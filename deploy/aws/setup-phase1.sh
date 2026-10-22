#!/usr/bin/env bash
# Provision the Phase-1 AWS footprint for the WhatsApp Payment platform.
#
# Creates the minimum viable 3-instance deployment described by the
# Phase-1 server plan (docs/cost-models/Xego_Server_and_Hosting_Plan.xlsx):
#
#   faya-edge-app1  t3.medium   public subnet   Caddy + app replica 1
#   faya-app2      t3.medium   private subnet  app replica 2 + workers
#   faya-db        m6i.large   private subnet  PostgreSQL + PgBouncer
#
# Everything is regional and therefore created in the project's selected
# Region (eu-north-1). IAM is global and is the only exception.
#
# Design notes:
#   - All three instances land in ONE availability zone. Phase 1 trades
#     cross-AZ resilience for ~$35/mo (a second NAT gateway plus inter-AZ
#     data transfer). The AZ spread plan belongs to Phase 2.
#   - The app and db hosts get NO public IP. The SSM agent dials out to AWS,
#     so no inbound SSH or bastion is needed and the GitHub Actions runners
#     (which have no stable egress IP) can still drive deployments.
#   - The edge host keeps a public IP because it terminates TLS for the
#     public ingress.
#   - A single NAT gateway in the public subnet gives the private hosts
#     outbound access for image pulls, S3 backups and certificate renewal.
#
# Usage:
#   AWS_PROFILE=tech.tricoxsys bash deploy/aws/setup-phase1.sh
#   AWS_PROFILE=tech.tricoxsys REPO_URL=git@github.com:org/repo.git \
#     ADMIN_IP=41.184.50.254 bash deploy/aws/setup-phase1.sh
#
# The script is idempotent: re-running it reuses existing resources by tag
# and only creates what is missing. It never destroys anything; teardown is a
# separate, explicit step (see deploy/aws/README.md).

set -Eeuo pipefail

# --------------------------------------------------------------- parameters --

PROFILE="${AWS_PROFILE:-tech.tricoxsys}"
REGION="${AWS_REGION:-eu-north-1}"
PROJECT="${PROJECT:-faya}"
PHASE="${PHASE:-1}"

VPC_CIDR="${VPC_CIDR:-10.20.0.0/16}"
PUBLIC_SUBNET_CIDR="${PUBLIC_SUBNET_CIDR:-10.20.0.0/24}"
PRIVATE_APP_SUBNET_CIDR="${PRIVATE_APP_SUBNET_CIDR:-10.20.1.0/24}"
PRIVATE_DB_SUBNET_CIDR="${PRIVATE_DB_SUBNET_CIDR:-10.20.2.0/24}"

EDGE_INSTANCE_TYPE="${EDGE_INSTANCE_TYPE:-t3.medium}"
APP_INSTANCE_TYPE="${APP_INSTANCE_TYPE:-t3.medium}"
# Matches the AWS Pricing Calculator estimate (Aws Pricing Estimate.pdf):
# 2 x t3.medium app hosts and 1 x m6i.large database host. Note that
# m6i.large is 2 vCPU / 8 GiB, whereas the server plan's Phase-1 database
# line is 4 vCPU / 8 GiB - keep an eye on CPU saturation and move to
# m6i.xlarge (4 vCPU / 16 GiB) if the DB host is consistently above ~60%.
DB_INSTANCE_TYPE="${DB_INSTANCE_TYPE:-m6i.large}"

EDGE_ROOT_GB="${EDGE_ROOT_GB:-40}"
APP_ROOT_GB="${APP_ROOT_GB:-40}"
DB_ROOT_GB="${DB_ROOT_GB:-50}"
DB_DATA_GB="${DB_DATA_GB:-240}"

# The operator's admin egress IP. Only used for the optional SSH rule; the
# SSM path does not need it. Override when the admin IP has changed.
ADMIN_IP="${ADMIN_IP:-}"

REPO_URL="${REPO_URL:-}"
GIT_REF="${GIT_REF:-main}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/whatsapp-payment}"
OUT_FILE="${OUT_FILE:-deploy/aws/phase1.env}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
USERDATA_APP="${SCRIPT_DIR}/userdata/app.sh"
USERDATA_DB="${SCRIPT_DIR}/userdata/db.sh"

# ------------------------------------------------------------------ helpers --

log() { printf '\n== %s ==\n' "$*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

awsx() { aws --profile "$PROFILE" --region "$REGION" "$@"; }

tag_spec() { # tag_spec <resource-type> <name>
  printf 'ResourceType=%s,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s},{Key=Phase,Value=%s},{Key=ManagedBy,Value=setup-phase1.sh}]' \
    "$1" "$2" "$PROJECT" "$PHASE"
}

role_tag_spec() { # role_tag_spec <name> <role>
  printf 'ResourceType=%s,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s},{Key=Role,Value=%s}]' \
    "$1" "$2" "$PROJECT" "$3"
}

# ---------------------------------------------------------------- preflight --

preflight() {
  log "preflight"
  command -v aws >/dev/null 2>&1 || die "aws CLI not found on PATH"
  if ! awsx sts get-caller-identity >/dev/null 2>&1; then
    die "no usable credentials for profile '$PROFILE'. Run: aws login --profile $PROFILE"
  fi
  ACCOUNT_ID="$(awsx sts get-caller-identity --query Account --output text)"
  echo "account  ${ACCOUNT_ID}"
  echo "region   ${REGION}"
  echo "profile  ${PROFILE}"
}

# ------------------------------------------------------------------- network --

ensure_vpc() {
  log "vpc"
  local vpc_id
  vpc_id="$(awsx ec2 describe-vpcs \
    --filters "Name=cidr,Values=${VPC_CIDR}" "Name=tag:Project,Values=${PROJECT}" \
    --query 'Vpcs[0].VpcId' --output text 2>/dev/null || true)"

  if [ -z "$vpc_id" ] || [ "$vpc_id" = "None" ]; then
    vpc_id="$(awsx ec2 create-vpc --cidr-block "$VPC_CIDR" \
      --tag-specifications "$(tag_spec vpc "${PROJECT}-phase${PHASE}-vpc")" \
      --query 'Vpc.VpcId' --output text)"
    echo "created vpc ${vpc_id}"
  else
    echo "reusing vpc ${vpc_id}"
  fi
  awsx ec2 modify-vpc-attribute --vpc-id "$vpc_id" --enable-dns-support '{"Value":true}'
  awsx ec2 modify-vpc-attribute --vpc-id "$vpc_id" --enable-dns-hostnames '{"Value":true}'
  VPC_ID="$vpc_id"
}

ensure_igw() {
  log "internet gateway"
  local igw_id
  igw_id="$(awsx ec2 describe-internet-gateways \
    --filters "Name=attachment.vpc-id,Values=${VPC_ID}" "Name=tag:Project,Values=${PROJECT}" \
    --query 'InternetGateways[0].InternetGatewayId' --output text 2>/dev/null || true)"
  if [ -z "$igw_id" ] || [ "$igw_id" = "None" ]; then
    igw_id="$(awsx ec2 create-internet-gateway \
      --tag-specifications "$(tag_spec internet-gateway "${PROJECT}-igw")" \
      --query 'InternetGateway.InternetGatewayId' --output text)"
    awsx ec2 attach-internet-gateway --vpc-id "$VPC_ID" --internet-gateway-id "$igw_id"
    echo "created igw ${igw_id}"
  else
    echo "reusing igw ${igw_id}"
  fi
  IGW_ID="$igw_id"
}

# Informational output goes to stderr so the id captured on stdout is clean.
ensure_subnet() { # ensure_subnet <cidr> <name> <az>
  local cidr="$1" name="$2" az="$3" subnet_id
  subnet_id="$(awsx ec2 describe-subnets \
    --filters "Name=cidr-block,Values=${cidr}" "Name=tag:Project,Values=${PROJECT}" \
    --query 'Subnets[0].SubnetId' --output text 2>/dev/null || true)"
  if [ -z "$subnet_id" ] || [ "$subnet_id" = "None" ]; then
    subnet_id="$(awsx ec2 create-subnet --vpc-id "$VPC_ID" --cidr-block "$cidr" \
      --availability-zone "$az" \
      --tag-specifications "$(tag_spec subnet "${name}")" \
      --query 'Subnet.SubnetId' --output text)"
    echo "created subnet ${name} ${subnet_id}" >&2
  else
    echo "reusing subnet ${name} ${subnet_id}" >&2
  fi
  echo "$subnet_id"
}

ensure_public_route_table() {
  PUBLIC_RT="$(awsx ec2 describe-route-tables \
    --filters "Name=tag:Project,Values=${PROJECT}" "Name=tag:Role,Values=public" \
    --query 'RouteTables[0].RouteTableId' --output text 2>/dev/null || true)"
  if [ -z "$PUBLIC_RT" ] || [ "$PUBLIC_RT" = "None" ]; then
    PUBLIC_RT="$(awsx ec2 create-route-table --vpc-id "$VPC_ID" \
      --tag-specifications "$(role_tag_spec route-table "${PROJECT}-public-rt" public)" \
      --query 'RouteTable.RouteTableId' --output text)"
    echo "created public route table ${PUBLIC_RT}"
  else
    echo "reusing public route table ${PUBLIC_RT}"
  fi
  awsx ec2 create-route --route-table-id "$PUBLIC_RT" --destination-cidr-block 0.0.0.0/0 \
    --gateway-id "$IGW_ID" >/dev/null 2>&1 || true
  awsx ec2 associate-route-table --route-table-id "$PUBLIC_RT" --subnet-id "$PUBLIC_SUBNET_ID" >/dev/null 2>&1 || true
}

ensure_nat() {
  log "nat gateway"
  local nat_id
  nat_id="$(awsx ec2 describe-nat-gateways \
    --filters "Name=tag:Project,Values=${PROJECT}" "Name=vpc-id,Values=${VPC_ID}" \
    --query 'NatGateways[?State!=`deleted`].[NatGatewayId] | [0]' --output text 2>/dev/null || true)"
  if [ -z "$nat_id" ] || [ "$nat_id" = "None" ]; then
    local alloc_id
    alloc_id="$(awsx ec2 describe-addresses --filters "Name=tag:Name,Values=${PROJECT}-nat-eip" --query 'Addresses[0].AllocationId' --output text 2>/dev/null || echo None)"
    if [ "$alloc_id" = "None" ] || [ -z "$alloc_id" ] || [ "$alloc_id" = "null" ]; then
      alloc_id="$(awsx ec2 allocate-address --domain vpc \
        --tag-specifications "$(tag_spec elastic-ip "${PROJECT}-nat-eip")" \
        --query 'AllocationId' --output text 2>&1 | grep -v '^aws:' | tail -1)"
    fi
    if [ -n "$alloc_id" ] && [ "$alloc_id" != "None" ] && [ "$alloc_id" != "null" ]; then
      nat_id="$(awsx ec2 create-nat-gateway --subnet-id "$PUBLIC_SUBNET_ID" \
        --allocation-id "$alloc_id" \
        --query 'NatGateway.NatGatewayId' --output text 2>&1 | grep -v '^aws:' | tail -1)"
      awsx ec2 create-tags --resources "$nat_id" --tags "Key=Name,Value=${PROJECT}-nat" "Key=Project,Value=$PROJECT" >/dev/null 2>&1 || true
      echo "created nat ${nat_id}; waiting for it to become available"
      awsx ec2 wait nat-gateway-available --nat-gateway-ids "$nat_id" 2>&1 | tail -2 || true
    else
      echo "could not allocate/get EIP for NAT; skipping NAT creation"
      NAT_ID=""
      return 0
    fi
  else
    echo "reusing nat ${nat_id}"
  fi
  NAT_ID="$nat_id"
}

ensure_private_route_table() {
  PRIVATE_RT="$(awsx ec2 describe-route-tables \
    --filters "Name=tag:Project,Values=${PROJECT}" "Name=tag:Role,Values=private" \
    --query 'RouteTables[0].RouteTableId' --output text 2>/dev/null || true)"
  if [ -z "$PRIVATE_RT" ] || [ "$PRIVATE_RT" = "None" ]; then
    PRIVATE_RT="$(awsx ec2 create-route-table --vpc-id "$VPC_ID" \
      --tag-specifications "$(role_tag_spec route-table "${PROJECT}-private-rt" private)" \
      --query 'RouteTable.RouteTableId' --output text)"
    echo "created private route table ${PRIVATE_RT}"
  else
    echo "reusing private route table ${PRIVATE_RT}"
  fi
  awsx ec2 create-route --route-table-id "$PRIVATE_RT" --destination-cidr-block 0.0.0.0/0 \
    --nat-gateway-id "$NAT_ID" >/dev/null 2>&1 || true
  awsx ec2 associate-route-table --route-table-id "$PRIVATE_RT" --subnet-id "$PRIVATE_APP_SUBNET_ID" >/dev/null 2>&1 || true
  awsx ec2 associate-route-table --route-table-id "$PRIVATE_RT" --subnet-id "$PRIVATE_DB_SUBNET_ID" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------- security groups --

ensure_sg() { # ensure_sg <name> <description>
  local name="$1" desc="$2" sg_id
  sg_id="$(awsx ec2 describe-security-groups \
    --filters "Name=group-name,Values=${name}" "Name=tag:Project,Values=${PROJECT}" \
    --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || true)"
  if [ -z "$sg_id" ] || [ "$sg_id" = "None" ]; then
    sg_id="$(awsx ec2 create-security-group --group-name "$name" --description "$desc" --vpc-id "$VPC_ID" \
      --tag-specifications "$(tag_spec security-group "${name}")" \
      --query 'GroupId' --output text)"
    echo "created sg ${name} ${sg_id}" >&2
  else
    echo "reusing sg ${name} ${sg_id}" >&2
  fi
  echo "$sg_id"
}

sg_allow() { # sg_allow <sg-id> <protocol> <port> <cidr>
  awsx ec2 authorize-security-group-ingress --group-id "$1" --protocol "$2" --port "$3" --cidr "$4" >/dev/null 2>&1 || true
}

sg_allow_sg() { # sg_allow_sg <sg-id> <protocol> <port> <source-sg-id>
  awsx ec2 authorize-security-group-ingress --group-id "$1" --protocol "$2" --port "$3" \
    --source-group "$4" >/dev/null 2>&1 || true
}

ensure_security_groups() {
  log "security groups"
  EDGE_SG="$(ensure_sg "${PROJECT}-edge-sg" "faya edge/ingress: 80,443 public")"
  APP_SG="$(ensure_sg "${PROJECT}-app-sg" "faya app: 8080 from edge only")"
  DB_SG="$(ensure_sg "${PROJECT}-db-sg" "faya db: 5432/6432 from app only")"
  OPS_SG="$(ensure_sg "${PROJECT}-ops-sg" "faya ops: 22 from admin ip only")"

  # Public ingress. 443 is opened on both TCP and UDP so Caddy can serve
  # HTTP/3 (QUIC) as compose.yaml publishes 443/udp.
  sg_allow "$EDGE_SG" tcp 80 0.0.0.0/0
  sg_allow "$EDGE_SG" tcp 443 0.0.0.0/0
  sg_allow "$EDGE_SG" udp 443 0.0.0.0/0

  # The app listens on :8080 (compose.yaml HTTP_ADDR) and is reached only by
  # the edge instances.
  sg_allow_sg "$APP_SG" tcp 8080 "$EDGE_SG"

  # PostgreSQL and PgBouncer, reachable only from the app security group.
  sg_allow_sg "$DB_SG" tcp 5432 "$APP_SG"
  sg_allow_sg "$DB_SG" tcp 6432 "$APP_SG"

  # Administrative SSH. Restricted to the operator IP; the release pipeline
  # uses SSM, so this exists only for manual sessions.
  if [ -n "$ADMIN_IP" ]; then
    sg_allow "$OPS_SG" tcp 22 "${ADMIN_IP}/32"
    echo "ops sg allows 22 from ${ADMIN_IP}/32"
  else
    echo "ADMIN_IP unset: no SSH ingress added. SSM/EC2 Instance Connect is the access path."
  fi
}

# ----------------------------------------------------------------------- iam --

ensure_iam() {
  log "iam (global)"
  local role_name="${PROJECT}-instance-role" profile_name="${PROJECT}-instance-profile"

  if ! awsx iam get-role --role-name "$role_name" >/dev/null 2>&1; then
    awsx iam create-role --role-name "$role_name" \
      --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
      --tags "Key=Name,Value=${role_name}" "Key=Project,Value=${PROJECT}" "Key=Phase,Value=${PHASE}" \
      --query 'Role.Arn' --output text
    echo "created role ${role_name}"
  else
    echo "reusing role ${role_name}"
  fi

  # SSM managed instance core: lets the SSM agent register the host and
  # lets the release pipeline drive it without SSH.
  awsx iam attach-role-policy --role-name "$role_name" \
    --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore >/dev/null 2>&1 || true

  if ! awsx iam get-instance-profile --instance-profile-name "$profile_name" >/dev/null 2>&1; then
    awsx iam create-instance-profile --instance-profile-name "$profile_name" \
      --tags "Key=Name,Value=${profile_name}" "Key=Project,Value=${PROJECT}" \
      --query 'InstanceProfile.InstanceProfileName' --output text
    awsx iam add-role-to-instance-profile --instance-profile-name "$profile_name" --role-name "$role_name"
    echo "created instance profile ${profile_name}"
  else
    echo "reusing instance profile ${profile_name}"
  fi
  INSTANCE_PROFILE="${profile_name}"
}

# ---------------------------------------------------------------------- ec2 --

resolve_ami() {
  log "ami"
  AMI_ID="$(awsx ssm get-parameter \
    --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
    --query 'Parameter.Value' --output text 2>/dev/null || echo ami-0111419d42d14a400)"
  echo "ami ${AMI_ID}"
}

# launch_instance <name> <type> <subnet-id> <sg-id> <root-gb> <public?> <userdata-file>
launch_instance() {
  local name="$1" itype="$2" subnet="$3" sg="$4" root_gb="$5" public_ip="$6" userdata="$7"

  local existing
  existing="$(awsx ec2 describe-instances \
    --filters "Name=tag:Name,Values=${name}" "Name=tag:Project,Values=${PROJECT}" \
    --query 'Reservations[0].Instances[0].InstanceId' --output text 2>/dev/null || true)"
  if [ -n "$existing" ] && [ "$existing" != "None" ]; then
    echo "reusing instance ${name} ${existing}" >&2
    echo "$existing"
    return
  fi

  local assign="false"
  [ "$public_ip" = "yes" ] && assign="true"

  local ud
  ud="$(mktemp /tmp/ud.XXXXXX 2>/dev/null || echo /tmp/ud.$$)"
  sed -e "s|@@REPO_URL@@|${REPO_URL}|g" \
      -e "s|@@GIT_REF@@|${GIT_REF}|g" \
      -e "s|@@DEPLOY_PATH@@|${DEPLOY_PATH}|g" \
      "$userdata" >"$ud"
  local udpath="$ud"
  # Convert to Windows path for Git Bash/Windows AWS CLI compatibility
  if command -v cygpath >/dev/null 2>&1; then
    udpath="$(cygpath -w "$ud")"
  elif [[ "$ud" == /tmp/* ]]; then
    udpath="C:/Users/User/AppData/Local/Temp/${ud##*/}"
    cp "$ud" "$udpath" 2>/dev/null || udpath="$ud"
  fi

  local id
  id="$(awsx ec2 run-instances \
    --image-id "$AMI_ID" \
    --instance-type "$itype" \
    --subnet-id "$subnet" \
    --security-group-ids "$sg" \
    --iam-instance-profile "arn:aws:iam::${ACCOUNT_ID}:instance-profile/${INSTANCE_PROFILE}" \
    $( [ "$assign" = "true" ] && echo --associate-public-ip-address ) \
    --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=${root_gb},VolumeType=gp3,Encrypted=true,DeleteOnTermination=true}" \
    --metadata-options "HttpTokens=required,HttpEndpoint=enabled,HttpPutResponseHopLimit=1" \
    --instance-initiated-shutdown-behavior terminate \
    --user-data "file://${udpath}" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${name}},{Key=Project,Value=${PROJECT}},{Key=Phase,Value=${PHASE}},{Key=ManagedBy,Value=setup-phase1.sh}]" \
    --query 'Instances[0].InstanceId' --output text 2>&1 | grep -v '^aws:' | tail -1)"
  rm -f "$ud" "$udpath" 2>/dev/null || true

  echo "created instance ${name} ${id}" >&2
  echo "$id"
}

wait_for_instances() {
  log "waiting for instances to reach running"
  # shellcheck disable=SC2086
  awsx ec2 wait instance-running --instance-ids $1
  # shellcheck disable=SC2086
  awsx ec2 wait instance-status-ok --instance-ids $1
  echo "all instances running"
}

wait_for_ssm() {
  log "waiting for SSM registration"
  for id in $1; do
    for _ in $(seq 1 40); do
      local pinged
      pinged="$(awsx ssm describe-instance-information --filters "Key=InstanceIds,Values=${id}" \
        --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || true)"
      if [ "$pinged" = "Online" ]; then
        echo "ssm online: ${id}"
        break
      fi
      sleep 5
    done
  done
}

# --------------------------------------------------------------------- main --

main() {
  preflight

  AZ="$(awsx ec2 describe-availability-zones --region "$REGION" \
    --query "AvailabilityZones[?State=='available'].ZoneName" --output text | tr '\t' '\n' | head -n1)"
  echo "availability zone ${AZ}"

  ensure_vpc
  ensure_igw

  PUBLIC_SUBNET_ID="$(ensure_subnet "$PUBLIC_SUBNET_CIDR" "${PROJECT}-public" "$AZ")"
  PRIVATE_APP_SUBNET_ID="$(ensure_subnet "$PRIVATE_APP_SUBNET_CIDR" "${PROJECT}-private-app" "$AZ")"
  PRIVATE_DB_SUBNET_ID="$(ensure_subnet "$PRIVATE_DB_SUBNET_CIDR" "${PROJECT}-private-db" "$AZ")"

  ensure_public_route_table
  ensure_nat
  ensure_private_route_table
  ensure_security_groups
  ensure_iam
  resolve_ami

  log "instances"
  EDGE_ID="$(launch_instance "${PROJECT}-edge-app1" "$EDGE_INSTANCE_TYPE" "$PUBLIC_SUBNET_ID" "$EDGE_SG" "$EDGE_ROOT_GB" yes "$USERDATA_APP")"
  APP_ID="$(launch_instance "${PROJECT}-app2" "$APP_INSTANCE_TYPE" "$PRIVATE_APP_SUBNET_ID" "$APP_SG" "$APP_ROOT_GB" no "$USERDATA_APP")"
  DB_ID="$(launch_instance "${PROJECT}-db" "$DB_INSTANCE_TYPE" "$PRIVATE_DB_SUBNET_ID" "$DB_SG" "$DB_ROOT_GB" no "$USERDATA_DB")"

  # The 220 GB data volume for PostgreSQL. DeleteOnTermination=false so a
  # rebuild of the host does not destroy the data plane.
  log "db data volume"
  DB_VOLUME_ID="$(awsx ec2 describe-volumes \
    --filters "Name=tag:Name,Values=${PROJECT}-db-data" "Name=tag:Project,Values=${PROJECT}" \
    --query 'Volumes[0].VolumeId' --output text 2>/dev/null || true)"
  if [ -z "$DB_VOLUME_ID" ] || [ "$DB_VOLUME_ID" = "None" ]; then
    DB_VOLUME_ID="$(awsx ec2 create-volume --availability-zone "$AZ" --size "$DB_DATA_GB" --volume-type gp3 --encrypted \
      --tag-specifications "ResourceType=volume,Tags=[{Key=Name,Value=${PROJECT}-db-data},{Key=Project,Value=${PROJECT}},{Key=Phase,Value=${PHASE}}]" \
      --query 'VolumeId' --output text)"
    echo "created volume ${DB_VOLUME_ID}"
  else
    echo "reusing volume ${DB_VOLUME_ID}"
  fi
  awsx ec2 attach-volume --volume-id "$DB_VOLUME_ID" --instance-id "$DB_ID" --device /dev/sdf >/dev/null 2>&1 || true
  awsx ec2 wait volume-in-use --volume-ids "$DB_VOLUME_ID"

  wait_for_instances "${EDGE_ID} ${APP_ID} ${DB_ID}"
  wait_for_ssm "${EDGE_ID} ${APP_ID} ${DB_ID}"

  log "addresses"
  awsx ec2 describe-instances --instance-ids ${EDGE_ID} ${APP_ID} ${DB_ID} \
    --query 'Reservations[].Instances[].[Tags[?Key==`Name`].Value|[0],InstanceId,InstanceType,PrivateIpAddress,PublicIpAddress]' \
    --output table

  EDGE_IP="$(awsx ec2 describe-instances --instance-ids "$EDGE_ID" --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)"
  APP_PRIV_IP="$(awsx ec2 describe-instances --instance-ids "$APP_ID" --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)"
  DB_PRIV_IP="$(awsx ec2 describe-instances --instance-ids "$DB_ID" --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)"

  mkdir -p "$(dirname "$OUT_FILE")"
  cat >"$OUT_FILE" <<EOF
# Generated by deploy/aws/setup-phase1.sh - contains no secrets.
AWS_PROFILE=${PROFILE}
AWS_REGION=${REGION}
AWS_ACCOUNT_ID=${ACCOUNT_ID}
XEGO_VPC_ID=${VPC_ID}
XEGO_EDGE_INSTANCE_ID=${EDGE_ID}
XEGO_APP_INSTANCE_ID=${APP_ID}
XEGO_DB_INSTANCE_ID=${DB_ID}
XEGO_EDGE_PUBLIC_IP=${EDGE_IP}
XEGO_APP_PRIVATE_IP=${APP_PRIV_IP}
XEGO_DB_PRIVATE_IP=${DB_PRIV_IP}
XEGO_EDGE_SG=${EDGE_SG}
XEGO_APP_SG=${APP_SG}
XEGO_DB_SG=${DB_SG}
XEGO_DEPLOY_PATH=${DEPLOY_PATH}
EOF
  echo "wrote ${OUT_FILE}"

  cat <<EOF

== Phase 1 AWS footprint is up ==

  edge   ${EDGE_ID}  ${EDGE_INSTANCE_TYPE}  public ${EDGE_IP}
  app    ${APP_ID}  ${APP_INSTANCE_TYPE}  private ${APP_PRIV_IP}
  db     ${DB_ID}  ${DB_INSTANCE_TYPE}  private ${DB_PRIV_IP}

Next:
  1. Point DNS for PUBLIC_HOST at ${EDGE_IP}
  2. Reach a host:  aws ssm start-session --target ${EDGE_ID}
  3. The repo's compose stack is still single-host. Splitting db/app across
     three hosts needs compose.db.yaml + compose.app.yaml and a DB-only mode
     in deploy/release.sh (it hard-requires an 'app' service at
     deploy/release.sh:278). See deploy/aws/README.md.
EOF
}

main "$@"
