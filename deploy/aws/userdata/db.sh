#!/usr/bin/env bash
# cloud-init user-data for the Phase-1 database host.
#
# Same runtime bootstrap as deploy/aws/userdata/app.sh, plus:
#   - formatting and mounting the 220 GB gp3 data volume at /data/postgres
#   - a persistent fstab entry so the volume re-mounts after a reboot
#   - ownership for the postgres UID (999) that the container runs as
#
# The data volume is attached with DeleteOnTermination=false, so the volume
# survives an instance stop/start and (with a snapshot) a rebuild.
#
# Placeholders substituted by deploy/aws/setup-phase1.sh:
#   @@REPO_URL@@   repository to clone (empty disables the clone)
#   @@GIT_REF@@    branch to check out initially
#   @@DEPLOY_PATH@@ checkout path used by deploy/release.sh
set -Eeuo pipefail

DEPLOY_PATH="@@DEPLOY_PATH@@"
REPO_URL="@@REPO_URL@@"
GIT_REF="@@GIT_REF@@"
DATA_MOUNT="${DATA_MOUNT:-/data/postgres}"
DATA_SIZE_BYTES="${DATA_SIZE_BYTES:-257698037760}" # 240 GiB

dnf update -y
dnf install -y docker git xfsprogs
systemctl enable --now docker
usermod -aG docker ec2-user

mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

mkdir -p /etc/docker
cat >/etc/docker/daemon.json <<'JSON'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "3" }
}
JSON
systemctl restart docker

# --- locate the data volume -------------------------------------------------
# The EBS volume is attached as a Nitro device (/dev/nvme1n1), but the name is
# not stable across instance types and re-attaches, so the device is resolved
# by size instead. The 50 GB root disk is intentionally excluded.
device=""
for _ in $(seq 1 30); do
  while read -r name size; do
    [ -n "$name" ] || continue
    [ "$size" = "$DATA_SIZE_BYTES" ] || continue
    device="/dev/$name"
    break 2
  done < <(lsblk -b -o NAME,SIZE | tail -n +2)
  sleep 2
done
[ -n "$device" ] || { echo "ERROR: data volume ($DATA_SIZE_BYTES bytes) not found" >&2; exit 1; }

# --- format once, mount always ---------------------------------------------
if ! blkid "$device" >/dev/null 2>&1; then
  mkfs -t xfs "$device"
fi

mkdir -p "$DATA_MOUNT"

# Mount by device id so a device-name change (nvme1n1 -> nvme2n1 after a
# re-attach) does not break the mount on the next boot.
dev_id="/dev/disk/by-id/$(ls -1 /dev/disk/by-id/ 2>/dev/null | grep -E 'nvme-Amazon_Elastic_Block_Store|^ebs' | head -n1)"
if [ -z "$dev_id" ] || [ ! -e "$dev_id" ]; then
  dev_id="$device"
fi

if ! grep -q "$DATA_MOUNT" /etc/fstab; then
  printf '%s %s xfs defaults,nofail,noatime 0 2\n' "$dev_id" "$DATA_MOUNT" >>/etc/fstab
fi
mount "$dev_id" "$DATA_MOUNT"

# The postgres container runs as UID/GID 999 (the standard postgres image).
chown -R 999:999 "$DATA_MOUNT"
chmod 750 "$DATA_MOUNT"

if [ -n "$REPO_URL" ]; then
  mkdir -p "$DEPLOY_PATH"
  git clone --depth 50 --branch "$GIT_REF" "$REPO_URL" "$DEPLOY_PATH" || {
    mkdir -p "$DEPLOY_PATH"
    git -C "$DEPLOY_PATH" init
    git -C "$DEPLOY_PATH" remote add origin "$REPO_URL"
    git -C "$DEPLOY_PATH" fetch --depth 50 origin "$GIT_REF"
    git -C "$DEPLOY_PATH" checkout "origin/$GIT_REF"
  }
  chown -R ec2-user:ec2-user "$DEPLOY_PATH"
fi

id deploy >/dev/null 2>&1 || useradd -m -s /bin/bash deploy
usermod -aG docker deploy
printf 'deploy ALL=(ALL) NOPASSWD: /usr/bin/docker, /usr/bin/docker compose\n' >/etc/sudoers.d/faya-deploy
chmod 440 /etc/sudoers.d/faya-deploy

echo "faya db user-data complete: data volume ${device} mounted at ${DATA_MOUNT}"
df -h "$DATA_MOUNT"
