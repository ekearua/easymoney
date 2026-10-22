# Phase 1 on AWS — 3-instance deployment

Provisioning guide for the Phase-1 footprint described in
`docs/cost-models/faya_Server_and_Hosting_Plan.xlsx`:

| Role | Instance | Type | Subnet | Runs |
| --- | --- | --- | --- | --- |
| Edge / ingress | `faya-edge-app1` | `t3.medium` (2 vCPU / 4 GB) | public | Caddy, app replica 1 |
| App | `faya-app2` | `t3.medium` (2 vCPU / 4 GB) | private | app replica 2, workers |
| Database | `faya-db` | `m6i.large` (2 vCPU / 8 GB) | private | PostgreSQL, PgBouncer, backups |

These types match the AWS Pricing Calculator estimate already in
`docs/cost-models/Aws Pricing Estimate.pdf` (2 × `t3.medium` + 1 ×
`m6i.large`, 50 GB DB root + 240 GB data volume, $239.48/mo).

> **Region conflict to settle first.** That estimate is priced for
> **Africa (Cape Town) = `af-south-1`**, but this repo's AWS profile and the
> Agent Toolkit rule both point at **eu-north-1 (Stockholm)**. Cape Town is
> far closer to Nigerian users (roughly 40–60 ms vs 120–160 ms), which
> matters for a payments webhook path. The script defaults to `eu-north-1`
> because that is the project's assigned Region; pass
> `AWS_REGION=af-south-1` if you decide Cape Town is the target. Do not mix
> the two — every regional resource has to be in one Region.

Region: **eu-north-1** (the project's selected Region — all regional resources
must live there). IAM is global and is the only exception.

---

## 1. Prerequisites

```bash
aws login --profile tech.tricoxsys          # the session lasts ~12h
aws sts get-caller-identity --profile tech.tricoxsys
```

The script refuses to run if the session is stale rather than hanging on a
browser sign-in.

You also need an SSH key pair **only** if you want key-based SSH; the script
does not create one because the deployment path is SSM, not SSH (see §6).

---

## 2. Run the provisioning script

```bash
AWS_PROFILE=tech.tricoxsys \
REPO_URL=git@github.com:ekearua/easymoney.git \
ADMIN_IP=41.184.50.254 \
bash deploy/aws/setup-phase1.sh
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `AWS_PROFILE` | `tech.tricoxsys` | credentials profile |
| `AWS_REGION` | `eu-north-1` | region for all regional resources |
| `ADMIN_IP` | *(empty)* | operator egress IP allowed to reach port 22. Empty means no SSH rule at all. |
| `REPO_URL` | *(empty)* | repo cloned to `DEPLOY_PATH` by the user-data. Empty skips the clone. |
| `GIT_REF` | `main` | branch checked out at first boot |
| `DEPLOY_PATH` | `/opt/whatsapp-payment` | checkout path `deploy/release.sh` expects |
| `PROJECT` | `xego` | name prefix + `Project` tag on every resource |

The script is **idempotent**: it finds resources by tag and only creates what
is missing, so it is safe to re-run after a partial failure. It never deletes
anything.

### What it creates

```
VPC 10.20.0.0/16
├── faya-public        10.20.0.0/24   (edge, public IP, default route -> IGW)
├── faya-private-app   10.20.1.0/24   (app,  no public IP, default route -> NAT)
└── faya-private-db    10.20.2.0/24   (db,   no public IP, default route -> NAT)
IGW faya-igw
NAT faya-nat  (in faya-public, 1 Elastic IP)
Security groups
├── faya-edge-sg   80,443/tcp + 443/udp from 0.0.0.0/0
├── faya-app-sg    8080/tcp    from faya-edge-sg
├── faya-db-sg     5432,6432   from faya-app-sg
└── faya-ops-sg    22          from ADMIN_IP/32 (only if ADMIN_IP is set)
IAM  faya-instance-role  +  faya-instance-profile
      policy AmazonSSMManagedInstanceCore
EC2   faya-edge-app1  t3.medium   40 GB gp3 root
      faya-app2      t3.medium   40 GB gp3 root
      faya-db        m6i.xlarge  50 GB gp3 root + 220 GB gp3 data volume
```

It writes `deploy/aws/phase1.env` with the instance IDs, private IPs and SG
IDs so later steps (and the pipeline) do not have to re-discover them.

### Deliberate choices

- **One availability zone.** Phase 1 trades cross-AZ resilience for roughly
  $35/mo (a second NAT gateway plus inter-AZ data transfer). Spread the
  instances across two AZs in Phase 2.
- **No public IP on the app and db hosts.** The SSM agent makes an *outbound*
  connection to AWS, so no inbound SSH rule is needed and the attack surface
  is the edge host only.
- **No key pair.** EC2 Instance Connect gives you a browser-based shell with
  temporary keys; there is no long-lived private key to leak.
- **IMDSv2 required** on every instance (`HttpTokens=required`).
- **gp3 everywhere** with encrypted volumes.

---

## 3. DNS

Point `PUBLIC_HOST` (currently `faya-test.kortiniafrica.com` in
`.env.example`) at the edge instance's public IP:

```bash
aws route53 list-hosted-zones --profile tech.tricoxsys
aws route53 change-resource-record-sets --profile tech.tricoxsys \
  --hosted-zone-id ZONEID \
  --change-batch '{"Changes":[{"Action":"UPSERT","ResourceRecordSet":{
    "Name":"faya-test.kortiniafrica.com.","Type":"A",
    "TTL":60,"ResourceRecords":[{"Value":"<EDGE_PUBLIC_IP>"}]}}]}'
```

Use a low TTL (60s) during cutover so a later IP change does not strand
traffic for the old TTL.

---

## 4. Security groups — what the IPs mean

| Rule | Source | Why |
| --- | --- | --- |
| 80 / 443 on `faya-edge-sg` | `0.0.0.0/0` | The public internet must reach the ingress. |
| 8080 on `faya-app-sg` | `faya-edge-sg` | Only Caddy proxies to the app. Referencing the SG (not an IP) means the rule survives instance changes. |
| 5432 / 6432 on `faya-db-sg` | `faya-app-sg` | Only app containers may reach PostgreSQL and PgBouncer. |
| 22 on `faya-ops-sg` | `ADMIN_IP/32` | Manual admin shell only. `ADMIN_IP` today is `41.184.50.254`. |

**443 is open on UDP as well as TCP** because `compose.yaml:213` publishes
`443:443/udp` so Caddy can serve HTTP/3 (QUIC). Dropping the UDP rule
silently disables HTTP/3 but does not break TLS.

Your admin IP changes if your ISP rotates it. If you lock yourself out, use
**EC2 Instance Connect** from the console (EC2 → Instances → Connect → EC2
Instance Connect), which bypasses the port 22 rule entirely.

### What a bastion host is, and why you do not need one

A bastion ("jump host") is a small server that is the single entry point into
a private network:

```
your laptop ──SSH──▶ bastion (public IP, port 22 open to you only)
                        │
                        └──SSH──▶ app, db (no public IP, port 22 open to the bastion only)
```

The point is that your app and database hosts get **no public IP at all** and
port 22 is open only from one tiny box, so there is one thing to harden
instead of three.

You do not need one here, because **GitHub Actions runners have no stable
egress IP**. A bastion cannot fix that: either you open port 22 to the world
and rely on key-only auth (scanners find it in minutes — not acceptable for a
PCI-scoped payments platform), or you drive the hosts over SSM instead. The
script provisions the SSM path.

---

## 5. Verify

```bash
. deploy/aws/phase1.env

# Instances running
aws ec2 describe-instances --profile tech.tricoxsys --region eu-north-1 \
  --instance-ids $faya_EDGE_INSTANCE_ID $faya_APP_INSTANCE_ID $faya_DB_INSTANCE_ID \
  --query 'Reservations[].Instances[].[InstanceId,State.Name]' --output table

# SSM registered (all three should be Online)
aws ssm describe-instance-information --profile tech.tricoxsys --region eu-north-1 \
  --query 'InstanceInformationList[].[InstanceId,PingStatus]' --output table

# Shell into the db host
aws ssm start-session --profile tech.tricoxsys --region eu-north-1 --target $faya_DB_INSTANCE_ID
#   then, inside the session:
#   docker --version && docker compose version
#   df -h /data/postgres          # expect the 220 GB volume
#   lsblk                        # expect a 220G xfs device

# Data volume is attached and in use
aws ec2 describe-volumes --profile tech.tricoxsys --region eu-north-1 \
  --filters Name=tag:Name,Values=faya-db-data \
  --query 'Volumes[].[VolumeId,Size,State,Encrypted]' --output table
```

Cloud-init logs if a host did not finish bootstrapping:

```bash
aws ssm start-session --target $faya_APP_INSTANCE_ID
#   tail -n 40 /var/log/cloud-init-output.log
```

---

## 6. Driving deployments — SSM instead of SSH

The current pipeline (`.github/workflows/release.yml:245`) reaches hosts over
SSH with `secrets.DEPLOY_SSH_KEY` and `secrets.DEPLOY_KNOWN_HOSTS`. That has
two problems on AWS:

1. GitHub Actions runners have no stable egress IP, so port 22 cannot be
   safely whitelisted.
2. The key lives in GitHub secrets and on every host.

The SSM path removes both. The release script does not change — it is the
same `deploy/release.sh` — only the transport does:

```bash
# What the SSH step becomes
aws ssm send-command \
  --instance-ids "$faya_EDGE_INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters commands='["cd /opt/whatsapp-payment && git fetch --tags --force --prune origin && git checkout --detach refs/tags/v1.2.3 && APP_IMAGE_REPO=ghcr.io/ekearua/easymoney bash deploy/release.sh v1.2.3"]' \
  --region eu-north-1
```

To run that from GitHub Actions you need:

1. **GitHub OIDC provider** in IAM (`token.actions.githubusercontent.com`),
   trusted on the `repo:ORG/REPO:ref:refs/tags/*` subject.
2. **A deploy role** the workflow assumes, with `AmazonSSMManagedInstanceCore`
   plus `ssm:SendCommand` / `ssm:GetCommandInvocation` on the three
   instances.
3. `permissions: id-token: write` in the workflow.

The `deploy` job then replaces the `Configure SSH` and `ssh` steps with
`aws ssm send-command` + a poll on `GetCommandInvocation`. The
`safe_ref` guards at `.github/workflows/release.yml:286` stay exactly as they
are — they still validate the tag and paths before anything reaches the
remote command.

**This is a change to `release.yml`, so it is not done yet.** It is the next
increment, after the instances exist.

---

## 7. Cost lines for the AWS Pricing Calculator (eu-north-1)

Add these so the estimate matches what the script actually creates:

| Line | Setting |
| --- | --- |
| EC2 edge | `t3.medium`, Linux, Shared tenancy, On-Demand, 730 h/mo |
| EC2 app | `t3.medium`, Linux, Shared tenancy, On-Demand, **2 instances**, 730 h/mo |
| EC2 db | `m6i.large`, Linux, Dedicated tenancy, On-Demand, 1 instance, 730 h/mo |
| EBS app roots | 2 × 40 GB gp3 |
| EBS db root | 50 GB gp3 |
| EBS db data | 240 GB gp3 (IOPS 3000, throughput 125 MiB/s baseline) |
| EBS snapshots | 2× daily, ~3 GB changed per snapshot |
| NAT gateway | 1 × NAT-Hours + data processing |
| Elastic IP | 1 attached to the NAT (free while in use) |
| Data transfer | egress to internet; **no inter-AZ charge** (single AZ) |
| Route 53 | hosted zone + DNS queries |

The existing estimate (`Aws Pricing Estimate.pdf`) totals **$239.48/mo** —
$107.22 for the `m6i.large` DB, $87.51 for the two `t3.medium` app hosts, and
$44.75 for the 240 GB gp3 volume with 2× daily snapshots. It does **not**
include the NAT gateway (about $32/mo plus data processing), Route 53, or
requester-pays egress, so the real number is nearer $275–285/mo.

Notes that affect the number:

- `t3.medium` is burstable. Under sustained load it bills the **baseline**
  rate plus the per-hour surplus credit charge. For a steady 2-vCPU app
  replica, `m6i.large` (2 vCPU / 8 GB, fixed rate) is often cheaper once the
  credits are exhausted. Model both.
- The estimate uses `m6i.large` for the DB, which is **2 vCPU / 8 GiB** — half
  the vCPU of the plan's 4 vCPU / 8 GB database line. Watch CPU; move to
  `m6i.xlarge` (4 vCPU / 16 GiB) if it sits above ~60%.
- gp3 baseline is 3000 IOPS and 125 MiB/s. Raise throughput to 250–500 MiB/s
  only if the metrics show sustained saturation.
- NAT gateway hourly charges are the single largest surprise for a
  3-instance setup.

---

## 8. Known gap — the compose stack is still single-host

The repository's compose topology does not yet match this 3-instance layout:

| File | Services | Intended host |
| --- | --- | --- |
| `compose.yaml` | db, pgbouncer, backup, backup-scheduler, migrate, app ×2, caddy | one host |
| `compose.edge.yaml` | app ×1, caddy | second host (dual-Caddy) |

`deploy/release.sh:278` hard-requires an `app` service:

```bash
has_service app || die "no 'app' service in '${COMPOSE_FILES}'; check COMPOSE_FILES"
```

So a database-only host cannot be rolled out by `release.sh` today. Two
options:

**Option A — keep the database on the app host (no code change).**
Run `compose.yaml` on `faya-edge-app1` and `compose.edge.yaml` on `faya-app2`.
This is the topology the repo already supports. Cost: the edge host runs
PostgreSQL *and* the app, which does not fit `t3.medium`; bump it to
`m6i.large` or `m6i.xlarge`, and the "2/4/40" edge spec in the plan is
already stale.

**Option B — split it properly (code change).**
Add `compose.db.yaml` (db, pgbouncer, backup, backup-scheduler, migrate) and
`compose.app.yaml` (app ×1, caddy), and give `release.sh` a DB-only mode
that skips the app image pull/recreate but still runs the pre-migrate backup
and the `migrate` one-shot. `release_test.sh` needs a matching case.

Option B is what the plan describes and what the calculator lines in §7
assume. Recommend B, sequenced as:

1. `compose.db.yaml` + `compose.app.yaml`
2. `release.sh` `RELEASE_TARGET=db|app` mode + `release_test.sh` coverage
3. Fix the `COMPOSE_FILES` gap at `deploy/release.sh:84` (the edge host never
   receives the variable) and the `APP_IMAGE_TOKEN` gap at `:81`
4. Re-point `DEPLOY_HOSTS` at the three instance IDs and switch the transport
   to SSM

---

## 9. Teardown

Nothing in the script deletes anything. To tear the footprint down by hand:

```bash
. deploy/aws/phase1.env

aws ec2 terminate-instances --profile tech.tricoxsys --region eu-north-1 \
  --instance-ids $faya_EDGE_INSTANCE_ID $faya_APP_INSTANCE_ID $faya_DB_INSTANCE_ID
aws ec2 delete-volume --profile tech.tricoxsys --region eu-north-1 \
  --volume-id "$(aws ec2 describe-volumes --profile tech.tricoxsys --region eu-north-1 \
    --filters Name=tag:Name,Values=faya-db-data --query 'Volumes[0].VolumeId' --output text)"
```

Terminate the instances first — the NAT gateway, Elastic IP, route tables,
subnets, IGW, security groups and the IAM role are released separately and
each has a `delete-*` command. Snapshot the DB volume before deleting it if
the data matters.
