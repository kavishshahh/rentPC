# Host setup and the first real VM rental

This is the checklist for milestone 2c/3: booting a real sandbox VM on your
Windows PC (and/or Mac) and renting it from the other machine.

## Windows 10/11 (Home or Pro), x86_64

1. **Virtualization in BIOS/UEFI**: VT-x / AMD-V enabled (Task Manager →
   Performance → CPU → "Virtualization: Enabled").
2. **Windows Hypervisor Platform**: *Turn Windows features on or off* →
   tick **Windows Hypervisor Platform** (and *Virtual Machine Platform*).
   Or as admin: `DISM /online /Enable-Feature /FeatureName:HypervisorPlatform /All`.
   Reboot.
3. **QEMU**: install from https://qemu.weilnetz.de/w64/ (pick the latest
   `qemu-w64-setup-*.exe`), then add `C:\Program Files\qemu` to PATH.
4. Build the agent (Go 1.24+):
   ```powershell
   cd agents\host; go build -o rentpc-host.exe .\cmd\rentpc-host
   .\rentpc-host.exe doctor
   ```
   `doctor` must show ✓ for *QEMU installed* and *Windows Hypervisor Platform*.
5. Register + run (token from the web app → Host → your machine):
   ```powershell
   .\rentpc-host.exe register --server https://<api> --token <token>
   .\rentpc-host.exe run            # driver defaults to qemu
   ```
   First run downloads the Ubuntu 24.04 cloud image (~600 MB, checksum
   verified) into `%AppData%\rentpc-host\images`.
6. Windows Defender / antivirus may prompt about `qemu-system-x86_64.exe`;
   allow it. Windows Firewall will ask about QEMU: **deny** inbound (the agent
   never needs inbound), it does not matter for the design.

Troubleshooting: `%AppData%\rentpc-host\sandboxes\<rental>\serial.log` is the
VM console; `qemu.log` has QEMU's own errors. "WHPX: failed to setup
partition" means the WHP feature is not active (step 2). Very slow boot
(> 3 min) means it fell back to software emulation — check `--accel`.

## macOS 13+ (Apple Silicon or Intel)

1. `brew install qemu`
2. `cd agents/host && go build -o rentpc-host ./cmd/rentpc-host && ./rentpc-host doctor`
   (expects ✓ *Hypervisor.framework available*; on Apple Silicon the agent
   uses `qemu-system-aarch64` with the `edk2-aarch64-code.fd` firmware from
   Homebrew and the **arm64** Ubuntu image).
3. Register and run as above.

## Linux (Ubuntu 22.04+)

`sudo apt install qemu-system-x86 qemu-utils`, make sure `/dev/kvm` is
accessible (`sudo usermod -aG kvm $USER`), then register and run.

## The first rental (milestone 3 checklist)

On the **renter** machine (the other computer), in the web app:

1. Rent the host for 1 hour. Status should go `provisioning` → `ready` in
   30–120 s (first boot of a cloud image + cloud-init).
2. Browser terminal: you land as `renter@sandbox`. Run:
   ```bash
   cat /etc/os-release | head -2        # Ubuntu 24.04 inside the VM
   nproc; free -m; df -h /              # the allocated CPUs/RAM/disk
   sudo apt-get update && sudo apt-get install -y python3-pip golang-go
   pip install --break-system-packages requests
   python3 -c "import requests; print(requests.get('https://api.github.com').status_code)"   # 200 via proxy
   go run <(echo 'package main; import "fmt"; func main(){ fmt.Println("go works") }') 2>/dev/null || true
   ```
3. **Isolation checks (must all fail)**:
   ```bash
   curl -m 5 http://10.0.2.2:8080/            # host via slirp gateway → no route
   curl -m 5 http://192.168.1.1/              # home router → 403 from proxy / no route
   curl -m 5 http://169.254.169.254/          # metadata → refused
   ls /mnt /media; mount | grep -E '9p|virtiofs'   # nothing from the host
   ```
4. Native SSH from the renter machine:
   ```bash
   export RENTPC_API_KEY=rpk_...              # Account → API keys
   chmod 600 rentpc_<id>.key
   ssh -i rentpc_<id>.key -o ProxyCommand="rentpc tunnel --server https://<api> <rental-id>" renter@rentpc
   ```
5. Terminate from the rental page. On the host, confirm
   `%AppData%\rentpc-host\sandboxes\` no longer contains the rental and the
   QEMU process is gone. Host dashboard shows the earning.

Record the outcome of each step in `docs/DEMO.md`.
