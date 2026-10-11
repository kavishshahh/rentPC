<#
.SYNOPSIS
  Provision the rentPC server on AWS: one t4g.small in us-east-1, tagged
  project=colosseum (instance AND disk), Docker Compose stack via user-data.

.USAGE
  $env:AWS_ACCESS_KEY_ID     = "AKIA..."
  $env:AWS_SECRET_ACCESS_KEY = "..."
  .\up.ps1 -RepoUrl "https://github.com/<you>/rentpc.git" -DatabaseUrl "postgresql://...neon.tech/neondb?sslmode=require"

  Optional: -Domain rentpc.example.com   (point an A record at the printed IP first -> automatic HTTPS)
            -InstanceType t4g.micro       (default t4g.small; nano is too small for the web build)
  Re-running is safe: it reuses the key pair / security group / Elastic IP if they exist.
#>
param(
  [Parameter(Mandatory)] [string] $RepoUrl,
  [Parameter(Mandatory)] [string] $DatabaseUrl,
  [string] $Domain = "",
  [ValidateSet("t4g.nano","t4g.micro","t4g.small")] [string] $InstanceType = "t4g.small",
  [string] $Name = "rentpc"
)
$ErrorActionPreference = "Stop"
$env:AWS_DEFAULT_REGION = "us-east-1"
$Region = "us-east-1"
$Tags = "project=colosseum,Name=$Name"

function Tagged($type) { "ResourceType=$type,Tags=[{Key=project,Value=colosseum},{Key=Name,Value=$Name}]" }

Write-Host "==> Checking AWS CLI and credentials"
aws sts get-caller-identity --output text | Out-Null

Write-Host "==> Latest Ubuntu 24.04 arm64 AMI"
$Ami = aws ssm get-parameter --name /aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id --query Parameter.Value --output text
Write-Host "    $Ami"

Write-Host "==> Key pair"
$KeyFile = "$PSScriptRoot\$Name-key.pem"
$existing = aws ec2 describe-key-pairs --key-names "$Name-key" --query "KeyPairs[0].KeyName" --output text 2>$null
if ($existing -ne "$Name-key") {
  aws ec2 create-key-pair --key-name "$Name-key" --key-type ed25519 --tag-specifications (Tagged "key-pair") --query KeyMaterial --output text | Out-File -Encoding ascii $KeyFile
  Write-Host "    saved $KeyFile (keep it; it's your SSH key to the box)"
} else { Write-Host "    reusing $Name-key" }

Write-Host "==> Security group (22 ssh, 80 http, 443 https)"
$Vpc = aws ec2 describe-vpcs --filters Name=is-default,Values=true --query "Vpcs[0].VpcId" --output text
$Sg = aws ec2 describe-security-groups --filters "Name=group-name,Values=$Name-sg" "Name=vpc-id,Values=$Vpc" --query "SecurityGroups[0].GroupId" --output text 2>$null
if (-not $Sg -or $Sg -eq "None") {
  $Sg = aws ec2 create-security-group --group-name "$Name-sg" --description "rentPC web+api" --vpc-id $Vpc --tag-specifications (Tagged "security-group") --query GroupId --output text
  foreach ($p in 22,80,443) { aws ec2 authorize-security-group-ingress --group-id $Sg --protocol tcp --port $p --cidr 0.0.0.0/0 | Out-Null }
}
Write-Host "    $Sg"

Write-Host "==> Elastic IP (stable public address)"
$Alloc = aws ec2 describe-addresses --filters "Name=tag:Name,Values=$Name" --query "Addresses[0].AllocationId" --output text 2>$null
if (-not $Alloc -or $Alloc -eq "None") {
  $Alloc = aws ec2 allocate-address --domain vpc --tag-specifications (Tagged "elastic-ip") --query AllocationId --output text
}
$Ip = aws ec2 describe-addresses --allocation-ids $Alloc --query "Addresses[0].PublicIp" --output text
Write-Host "    $Ip"

Write-Host "==> Rendering user-data"
$ud = Get-Content "$PSScriptRoot\user-data.sh" -Raw
$ud = $ud.Replace("__REPO_URL__", $RepoUrl).Replace("__DATABASE_URL__", $DatabaseUrl).Replace("__DOMAIN__", $Domain).Replace("__PUBLIC_IP__", $Ip)
$udFile = "$env:TEMP\rentpc-user-data.sh"
[IO.File]::WriteAllText($udFile, $ud.Replace("`r`n","`n"))

Write-Host "==> Launching $InstanceType"
$InstanceId = aws ec2 run-instances `
  --image-id $Ami --instance-type $InstanceType --key-name "$Name-key" --security-group-ids $Sg `
  --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=20,VolumeType=gp3,DeleteOnTermination=true}" `
  --tag-specifications (Tagged "instance") (Tagged "volume") `
  --user-data "file://$udFile" `
  --query "Instances[0].InstanceId" --output text
Write-Host "    $InstanceId - waiting for it to be running"
aws ec2 wait instance-running --instance-ids $InstanceId

Write-Host "==> Attaching Elastic IP"
aws ec2 associate-address --instance-id $InstanceId --allocation-id $Alloc | Out-Null

Write-Host ""
Write-Host "Instance : $InstanceId ($InstanceType, $Region)"
Write-Host "Public IP: $Ip"
Write-Host "SSH      : ssh -i `"$KeyFile`" ubuntu@$Ip"
Write-Host "Setup log: ssh in and run  sudo tail -f /var/log/rentpc-setup.log"
if ($Domain) { Write-Host "Site     : https://$Domain   (create an A record $Domain -> $Ip now; HTTPS issues itself on first visit)" }
else         { Write-Host "Site     : http://$Ip        (first build takes ~8-10 minutes on a t4g.small)" }
Write-Host ""
