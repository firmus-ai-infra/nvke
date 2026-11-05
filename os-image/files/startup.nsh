@echo -off
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Boots Ubuntu when the firmware starts its built-in EFI shell instead of the
# disk. Some firmware, GB300 trays among it, re-creates its EFI shell boot
# entry at every POST and orders a freshly imaged disk behind it, so the first
# boot after imaging lands here. The shell runs startup.nsh from the root of a
# mapped filesystem; provision.sh puts this file at the root of the ESP
# (/boot/efi/startup.nsh). Once the OS is up, nvke-boot-order.service moves the
# disk ahead of the shell for later boots.
#
# Each loop scans every mapped filesystem before the next loader is tried, so
# Ubuntu's own loader on this disk wins over a removable-media loader on
# virtual media. shim comes first, then GRUB without shim. The removable-media
# path is last: shim started as BOOT<arch>.EFI runs fallback (fb<arch>.efi),
# which rewrites the boot entries and resets the node before it boots.
echo "nvke: the firmware started the EFI shell; booting Ubuntu from the ESP"
for %m in 0 1 2 3 4 5 6 7 8 9
  if exist fs%m:\EFI\ubuntu\shimaa64.efi then
    fs%m:
    \EFI\ubuntu\shimaa64.efi
  endif
  if exist fs%m:\EFI\ubuntu\shimx64.efi then
    fs%m:
    \EFI\ubuntu\shimx64.efi
  endif
endfor
for %m in 0 1 2 3 4 5 6 7 8 9
  if exist fs%m:\EFI\ubuntu\grubaa64.efi then
    fs%m:
    \EFI\ubuntu\grubaa64.efi
  endif
  if exist fs%m:\EFI\ubuntu\grubx64.efi then
    fs%m:
    \EFI\ubuntu\grubx64.efi
  endif
endfor
for %m in 0 1 2 3 4 5 6 7 8 9
  if exist fs%m:\EFI\BOOT\BOOTAA64.EFI then
    fs%m:
    \EFI\BOOT\BOOTAA64.EFI
  endif
  if exist fs%m:\EFI\BOOT\BOOTX64.EFI then
    fs%m:
    \EFI\BOOT\BOOTX64.EFI
  endif
endfor
echo "nvke: no Ubuntu loader found on fs0-fs9; staying in the EFI shell"
