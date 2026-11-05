// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package files_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootOrderScript is the script under test, relative to this package.
var bootOrderScript = func() string {
	abs, err := filepath.Abs(filepath.Join("..", "boot-order.sh"))
	if err != nil {
		panic(err)
	}
	return abs
}()

// espUUID is the ESP's GPT partition UUID the fake lsblk reports.
const espUUID = "0f3c1a2b-1111-4222-8333-444455556666"

// Boot entries as efibootmgr -v prints them: label, a tab, the device path.
const (
	httpV4    = "Boot0001* UEFI: HTTP IPv4 Mellanox Network Adapter - 00:00:5E:00:53:01\tPciRoot(0x0)/Pci(0x0,0x0)/MAC(00005e005301,0x1)/IPv4(0.0.0.0,0x0,DHCP,0.0.0.0,0.0.0.0,0.0.0.0)/Uri()"
	httpV6    = "Boot0002* UEFI: HTTP IPv6 Mellanox Network Adapter - 00:00:5E:00:53:01\tPciRoot(0x0)/Pci(0x0,0x0)/MAC(00005e005301,0x1)/IPv6([::],0x0,Static,[::],[::],0x40)/Uri()"
	bmcUSB    = "Boot0003* UEFI: HTTP IPv4 BMC USB NIC\tPciRoot(0x1)/Pci(0x0,0x0)/USB(0x1,0x0)/MAC(00005e005302,0x1)/IPv4(0.0.0.0,0x0,DHCP,0.0.0.0,0.0.0.0,0.0.0.0)/Uri()"
	disk      = "Boot0004* ubuntu\tHD(1,GPT," + espUUID + ",0x800,0x219800)/File(\\EFI\\ubuntu\\shimaa64.efi)"
	efiShell  = "Boot0005* UEFI: Built-in EFI Shell\tFvVol(5c60f367-a505-419a-859e-2a4ff6ca6fe5)/FvFile(7c04a583-9e3e-4f1c-ad65-e05268d0b4d1)"
	otherDisk = "Boot0006* ubuntu\tHD(1,GPT,9a8b7c6d-0000-4000-8000-000000000001,0x800,0x219800)/File(\\EFI\\ubuntu\\shimaa64.efi)"
	pxeV4     = "Boot0007* UEFI PXEv4 (MAC:00005E005303)\tPciRoot(0x0)/Pci(0x2,0x0)/MAC(00005e005303,0x1)/IPv4(0.0.0.0,0x0,DHCP,0.0.0.0,0.0.0.0,0.0.0.0)"
	diskPart2 = "Boot0008* ubuntu\tHD(2,GPT," + espUUID + ",0x100800,0x219800)/File(\\EFI\\ubuntu\\shimaa64.efi)"
)

// Fakes for the tools the script calls. efibootmgr reads and rewrites a state
// file in efibootmgr -v's format and logs every call; -c appends Boot00FF and
// prepends it to BootOrder, as efibootmgr does.
const (
	fakeEfibootmgr = `#!/bin/sh
set -eu
state="${FAKE_EFIBOOTMGR_STATE}"
echo "$*" >>"${FAKE_EFIBOOTMGR_LOG}"
case "$1" in
  -v) cat "${state}" ;;
  -o) awk -v o="$2" '/^BootOrder:/ { print "BootOrder: " o; next } { print }' "${state}" >"${state}.new"
      mv "${state}.new" "${state}" ;;
  -c) label=""; loader=""
      while [ $# -gt 0 ]; do
        case "$1" in -L) label=$2; shift ;; -l) loader=$2; shift ;; esac
        shift
      done
      awk '/^BootOrder:/ { sub(/^BootOrder: */, ""); print "BootOrder: 00FF" ($0 == "" ? "" : "," $0); next } { print }' "${state}" >"${state}.new"
      printf 'Boot00FF* %s\tHD(1,GPT,%s,0x800,0x219800)/File(%s)\n' "${label}" "${FAKE_ESP_PARTUUID}" "${loader}" >>"${state}.new"
      mv "${state}.new" "${state}" ;;
  *) exit 2 ;;
esac
`
	fakeFindmnt = `#!/bin/sh
[ -n "${FAKE_ESP_DEV}" ] || exit 1
echo "${FAKE_ESP_DEV}"
`
	fakeLsblk = `#!/bin/sh
col=""
while [ $# -gt 0 ]; do
  case "$1" in -o) col=$2; shift ;; esac
  shift
done
case "${col}" in
  PARTUUID) echo "${FAKE_ESP_PARTUUID}" ;;
  PKNAME) echo nvme0n1 ;;
  PARTN) echo 1 ;;
esac
`
)

// firmware is the fake node a test runs the script against.
type firmware struct {
	order   string   // BootOrder, comma-separated
	entries []string // Boot#### lines
	noEFI   bool     // no efivarfs, as on a BIOS boot
	noESP   bool     // nothing mounted at the ESP mount point
	dryRun  bool
}

type bootOrderResult struct {
	stdout, stderr string
	err            error
	order          string   // BootOrder after the run
	calls          []string // efibootmgr argument lists, in order
}

func runBootOrder(t *testing.T, fw firmware) bootOrderResult {
	t.Helper()
	root := t.TempDir()

	tools := filepath.Join(root, "tools")
	require.NoError(t, os.MkdirAll(tools, 0o755))
	for name, content := range map[string]string{"efibootmgr": fakeEfibootmgr, "findmnt": fakeFindmnt, "lsblk": fakeLsblk} {
		require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte(content), 0o755))
	}

	state := filepath.Join(root, "efibootmgr-state")
	lines := append([]string{"BootCurrent: 0005", "Timeout: 5 seconds", "BootOrder: " + fw.order}, fw.entries...)
	require.NoError(t, os.WriteFile(state, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	callLog := filepath.Join(root, "efibootmgr-calls")
	require.NoError(t, os.WriteFile(callLog, nil, 0o644))

	efivars := filepath.Join(root, "efivars")
	if !fw.noEFI {
		require.NoError(t, os.MkdirAll(efivars, 0o755))
	}
	esp := filepath.Join(root, "esp")
	require.NoError(t, os.MkdirAll(filepath.Join(esp, "EFI", "ubuntu"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(esp, "EFI", "ubuntu", "shimaa64.efi"), []byte("shim"), 0o644))
	espDev := "/dev/nvme0n1p1"
	if fw.noESP {
		espDev = ""
	}
	dryRun := "0"
	if fw.dryRun {
		dryRun = "1"
	}

	cmd := exec.CommandContext(t.Context(), "bash", bootOrderScript)
	cmd.Env = append(os.Environ(),
		"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NVKE_BOOT_ORDER_EFIVARS_DIR="+efivars,
		"NVKE_BOOT_ORDER_ESP_MOUNT="+esp,
		"NVKE_BOOT_ORDER_MACHINE=aarch64",
		"NVKE_BOOT_ORDER_DRY_RUN="+dryRun,
		"FAKE_EFIBOOTMGR_STATE="+state,
		"FAKE_EFIBOOTMGR_LOG="+callLog,
		"FAKE_ESP_DEV="+espDev,
		// lsblk prints lower case; upper case proves the script does not depend on it.
		"FAKE_ESP_PARTUUID="+strings.ToUpper(espUUID),
	)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	res := bootOrderResult{err: cmd.Run()}
	res.stdout, res.stderr = out.String(), errOut.String()

	content, err := os.ReadFile(state)
	require.NoError(t, err)
	for _, line := range strings.Split(string(content), "\n") {
		if order, ok := strings.CutPrefix(line, "BootOrder: "); ok {
			res.order = order
		}
	}
	calls, err := os.ReadFile(callLog)
	require.NoError(t, err)
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if call != "" {
			res.calls = append(res.calls, call)
		}
	}
	return res
}

func TestBootOrder(t *testing.T) {
	gb300 := []string{httpV4, httpV6, bmcUSB, disk, efiShell}

	tests := []struct {
		name     string
		firmware firmware
		want     string // BootOrder after the run
		written  bool   // whether efibootmgr -o was called
	}{
		{
			name:     "a freshly imaged disk behind the shell moves ahead of it",
			firmware: firmware{order: "0001,0002,0005,0003,0004", entries: gb300},
			want:     "0001,0002,0004,0003,0005",
			written:  true,
		},
		{
			name:     "an order that is already right is not rewritten",
			firmware: firmware{order: "0001,0002,0004,0003,0005", entries: gb300},
			want:     "0001,0002,0004,0003,0005",
		},
		{
			name:     "an HTTP entry after the disk is not promoted ahead of it",
			firmware: firmware{order: "0004,0003,0005", entries: gb300},
			want:     "0004,0003,0005",
		},
		{
			name:     "an HTTP entry behind another entry stays behind the disk",
			firmware: firmware{order: "0005,0003,0004", entries: gb300},
			want:     "0004,0003,0005",
			written:  true,
		},
		{
			name:     "leading PXE entries stay first, as HTTP ones do",
			firmware: firmware{order: "0007,0005,0004", entries: []string{pxeV4, disk, efiShell}},
			want:     "0007,0004,0005",
			written:  true,
		},
		{
			name:     "this disk's entry missing from BootOrder is added after the network entries",
			firmware: firmware{order: "0001,0005", entries: []string{httpV4, disk, efiShell}},
			want:     "0001,0004,0005",
			written:  true,
		},
		{
			name:     "an ESP that is not partition 1 is still this disk",
			firmware: firmware{order: "0001,0005,0008", entries: []string{httpV4, efiShell, diskPart2}},
			want:     "0001,0008,0005",
			written:  true,
		},
		{
			name:     "another disk's Ubuntu entry keeps its place behind this disk",
			firmware: firmware{order: "0001,0006,0005,0004", entries: []string{httpV4, otherDisk, efiShell, disk}},
			want:     "0001,0004,0006,0005",
			written:  true,
		},
		{
			name:     "a dry run changes nothing",
			firmware: firmware{order: "0001,0002,0005,0003,0004", entries: gb300, dryRun: true},
			want:     "0001,0002,0005,0003,0004",
		},
		{
			name:     "without UEFI variables nothing is read or changed",
			firmware: firmware{order: "0005,0004", entries: gb300, noEFI: true},
			want:     "0005,0004",
		},
		{
			name:     "without an ESP mounted nothing is changed",
			firmware: firmware{order: "0005,0004", entries: gb300, noESP: true},
			want:     "0005,0004",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runBootOrder(t, tt.firmware)
			require.NoError(t, res.err, res.stderr)
			assert.Equal(t, tt.want, res.order)

			var writes []string
			for _, call := range res.calls {
				if strings.HasPrefix(call, "-o ") {
					writes = append(writes, call)
				}
			}
			if tt.written {
				assert.Equal(t, []string{"-o " + tt.want}, writes)
			} else {
				assert.Empty(t, writes)
			}
			if tt.firmware.noEFI {
				assert.Empty(t, res.calls, "efibootmgr ran without UEFI variables")
			}
		})
	}
}

func TestBootOrderCreatesAnEntryForTheESP(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		order   string
		want    string
	}{
		{
			name:    "no entry for any disk",
			entries: []string{httpV4, httpV6, efiShell},
			order:   "0001,0002,0005",
			want:    "0001,0002,00FF,0005",
		},
		{
			name:    "an entry for another disk only",
			entries: []string{httpV4, otherDisk, efiShell},
			order:   "0001,0005,0006",
			want:    "0001,00FF,0006,0005",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runBootOrder(t, firmware{order: tt.order, entries: tt.entries})
			require.NoError(t, res.err, res.stderr)
			assert.Contains(t, res.calls, `-c -d /dev/nvme0n1 -p 1 -L ubuntu -l \EFI\ubuntu\shimaa64.efi`)
			// The new entry is prepended to BootOrder; it must not end up ahead
			// of the network entries that were leading before it existed.
			assert.Equal(t, tt.want, res.order)
		})
	}
}

func TestBootOrderDryRunDoesNotCreateAnEntry(t *testing.T) {
	res := runBootOrder(t, firmware{order: "0001,0005", entries: []string{httpV4, efiShell}, dryRun: true})
	require.NoError(t, res.err, res.stderr)
	assert.Contains(t, res.stdout, "DRY-RUN: would create boot entry 'ubuntu'")
	assert.Equal(t, []string{"-v"}, res.calls)
	assert.Equal(t, "0001,0005", res.order)
}

func TestStartupNshBootsUbuntusLoader(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "startup.nsh"))
	require.NoError(t, err)
	script := string(content)

	// The shell echoes every line it runs until echo is turned off.
	assert.True(t, strings.HasPrefix(script, "@echo -off\n"), "startup.nsh must start with @echo -off")
	// shim before GRUB before the removable-media path, which runs fallback.
	shim := strings.Index(script, `\EFI\ubuntu\shimaa64.efi`)
	grub := strings.Index(script, `\EFI\ubuntu\grubaa64.efi`)
	removable := strings.Index(script, `\EFI\BOOT\BOOTAA64.EFI`)
	require.NotEqual(t, -1, shim)
	require.NotEqual(t, -1, grub)
	require.NotEqual(t, -1, removable)
	assert.Less(t, shim, grub)
	assert.Less(t, grub, removable)
	for _, loader := range []string{`\EFI\ubuntu\shimx64.efi`, `\EFI\ubuntu\grubx64.efi`, `\EFI\BOOT\BOOTX64.EFI`} {
		assert.Contains(t, script, loader)
	}
}
