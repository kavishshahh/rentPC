# Tear down everything up.ps1 created (instance, EIP, security group, key pair).
param([string] $Name = "rentpc")
$ErrorActionPreference = "Continue"
$env:AWS_DEFAULT_REGION = "us-east-1"
$ids = aws ec2 describe-instances --filters "Name=tag:Name,Values=$Name" "Name=instance-state-name,Values=pending,running,stopping,stopped" --query "Reservations[].Instances[].InstanceId" --output text
if ($ids) { aws ec2 terminate-instances --instance-ids $ids.Split() | Out-Null; aws ec2 wait instance-terminated --instance-ids $ids.Split() }
$alloc = aws ec2 describe-addresses --filters "Name=tag:Name,Values=$Name" --query "Addresses[0].AllocationId" --output text
if ($alloc -and $alloc -ne "None") { aws ec2 release-address --allocation-id $alloc }
$sg = aws ec2 describe-security-groups --filters "Name=group-name,Values=$Name-sg" --query "SecurityGroups[0].GroupId" --output text
if ($sg -and $sg -ne "None") { aws ec2 delete-security-group --group-id $sg }
aws ec2 delete-key-pair --key-name "$Name-key"
Write-Host "Done."
