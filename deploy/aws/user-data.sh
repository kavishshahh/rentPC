#!/bin/bash
# EC2 user-data: first-boot setup for the rentPC box (Ubuntu 24.04 arm64).
# Placeholders __REPO_URL__, __DATABASE_URL__, __DOMAIN__, __PUBLIC_IP__ are filled in by up.ps1.
set -euxo pipefail
exec > >(tee /var/log/rentpc-setup.log) 2>&1

# 2 GB swap: the Next.js build needs more than the 2 GB RAM of a t4g.small.
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

apt-get update
apt-get install -y ca-certificates curl git
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=arm64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
usermod -aG docker ubuntu

cd /opt
git clone "__REPO_URL__" rentpc
chown -R ubuntu:ubuntu rentpc
cd rentpc/deploy/aws

PUBLIC_IP="__PUBLIC_IP__"
DOMAIN="__DOMAIN__"
if [ -n "$DOMAIN" ]; then
  PUBLIC_URL="https://$DOMAIN"; SITE_ADDRESS="$DOMAIN"; COOKIE_SECURE=true
else
  PUBLIC_URL="http://$PUBLIC_IP"; SITE_ADDRESS=":80"; COOKIE_SECURE=false
fi
cat > .env <<EOF
DATABASE_URL=__DATABASE_URL__
PUBLIC_URL=$PUBLIC_URL
SITE_ADDRESS=$SITE_ADDRESS
COOKIE_SECURE=$COOKIE_SECURE
EOF
chmod 600 .env

docker compose up -d --build
echo "rentPC is up at $PUBLIC_URL"
