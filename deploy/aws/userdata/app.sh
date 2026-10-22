#!/usr/bin/env bash
# cloud-init user-data for the Phase-1 application hosts (edge + app replica).
#
# Installs the Docker runtime and the Compose v2 plugin, clones the repository
# at DEPLOY_PATH so deploy/release.sh can `git fetch`/`git checkout --detach`
# a tag, and leaves the host ready for the release pipeline.
#
# The SSM agent is preinstalled on the Amazon Linux 2023 AMI, so no inbound
# SSH is required to reach the host afterwards (see deploy/aws/README.md).
#
# Placeholders substituted by deploy/aws/setup-phase1.sh:
#   @@REPO_URL@@   repository to clone (empty disables the clone)
#   @@GIT_REF@@    branch to check out initially
#   @@DEPLOY_PATH@@ checkout path used by deploy/release.sh
set -Eeuo pipefail

DEPLOY_PATH="@@DEPLOY_PATH@@"
REPO_URL="@@REPO_URL@@"
GIT_REF="@@GIT_REF@@"

dnf update -y
dnf install -y docker git
systemctl enable --now docker
usermod -aG docker ec2-user

# Docker Compose v2 plugin (the `docker compose` subcommand used by
# compose.yaml and deploy/release.sh). The AL2023 docker package does not
# ship it, so it is installed from the upstream release.
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

# Docker daemon defaults for a small app host: the compose stack limits the
# app replica to 1 CPU / 1 GB, so cap the log driver to avoid filling the
# root volume with container logs.
mkdir -p /etc/docker
cat >/etc/docker/daemon.json <<'JSON'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "3" }
}
JSON
systemctl restart docker

# Repository checkout. release.sh runs `git fetch --tags` + `git checkout
# --detach refs/tags/<vX.Y.Z>` from DEPLOY_PATH, so the clone must exist and
# the deploy user must own it.
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

# A deploy user for the release pipeline, with passwordless sudo for the
# docker group only. With SSM/EC2 Instance Connect the pipeline never needs
# the SSH key, but this user is the target for any manual `ssh` session.
id deploy >/dev/null 2>&1 || useradd -m -s /bin/bash deploy
usermod -aG docker deploy
printf 'deploy ALL=(ALL) NOPASSWD: /usr/bin/docker, /usr/bin/docker compose\n' >/etc/sudoers.d/faya-deploy
chmod 440 /etc/sudoers.d/faya-deploy

echo "faya user-data complete: docker $(docker --version), compose $(docker compose version --short 2>/dev/null || echo unknown)"
