#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Keeps this disk ahead of the firmware's built-in EFI shell in the UEFI boot
# order. Run at every boot by nvke-boot-order.service. See os-image/SPEC.md.
#
# Some firmware, GB300 trays among it, re-creates its "UEFI: Built-in EFI
# Shell" boot entry at every POST and orders a freshly imaged disk behind it.
# The entry can be neither deleted nor disabled over Redfish. The first boot
# after imaging therefore lands in the shell, where startup.nsh on the ESP
# starts Ubuntu, and so does every later reboot until BootOrder changes. This
# script rewrites BootOrder as:
#
#   1. the network (HTTP or PXE) entries at the head of BootOrder, unchanged,
#      so the site can still reimage the node;
#   2. this disk's entries;
#   3. every other entry, in its current relative order;
#   4. EFI shell entries.
#
# The leading network block ends at the first entry that is not a network
# entry, this disk's included. A network entry found later, such as a BMC USB
# NIC, is never promoted: every network entry ahead of the disk is a failed
# boot attempt on every reboot.
#
# This disk's entries are the ones whose device path names the ESP's GPT
# partition UUID, HD(<n>,GPT,<uuid>,...). BootCurrent cannot identify them:
# after a startup.nsh boot it is the shell. When no entry names the ESP, one is
# created for Ubuntu's loader on it.
#
# BootOrder is written only when it would change. Without UEFI variables, or
# without a GPT ESP mounted at /boot/efi, nothing changes.
# NVKE_BOOT_ORDER_DRY_RUN=1 prints the change instead of making it.
#
# Runs under bash 3.2 as well, so the tests pass on macOS: no associative
# arrays, no case-changing expansions, and every possibly empty array is
# expanded through ${a[@]+"${a[@]}"}.

set -euo pipefail

# Overridable for tests; production reads efivarfs and the installed ESP.
EFIVARS_DIR="${NVKE_BOOT_ORDER_EFIVARS_DIR:-/sys/firmware/efi/efivars}"
ESP_MOUNT="${NVKE_BOOT_ORDER_ESP_MOUNT:-/boot/efi}"
MACHINE="${NVKE_BOOT_ORDER_MACHINE:-$(uname -m)}"
DRY_RUN="${NVKE_BOOT_ORDER_DRY_RUN:-0}"

log() { echo "nvke-boot-order: $*"; }
upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
join_ids() {
  local IFS=,
  printf '%s' "$*"
}

# in_list ID [ID...]: whether the first argument is one of the rest.
in_list() {
  local want=$1 id
  shift
  for id in "$@"; do
    if [[ "${id}" == "${want}" ]]; then
      return 0
    fi
  done
  return 1
}

# Boot entries, indexed by the decimal value of their hex Boot#### number.
order=()
entries=()
labels=()
details=()

read_entries() {
  local out line id idx rest tab=$'\t'
  local order_re='^BootOrder:[[:space:]]*(.*)$'
  local entry_re='^Boot([0-9A-Fa-f]{4})\*?[[:space:]]+(.*)$'
  if ! out="$(efibootmgr -v)"; then
    log "efibootmgr -v failed" >&2
    exit 1
  fi
  order=()
  entries=()
  labels=()
  details=()
  while IFS= read -r line; do
    if [[ "${line}" =~ ${order_re} ]]; then
      rest="$(upper "${BASH_REMATCH[1]}")"
      rest="${rest//[[:space:]]/}"
      if [[ -n "${rest}" ]]; then
        IFS=, read -r -a order <<<"${rest}"
      fi
    elif [[ "${line}" =~ ${entry_re} ]]; then
      id="$(upper "${BASH_REMATCH[1]}")"
      rest="${BASH_REMATCH[2]}"
      idx=$((16#${id}))
      entries+=("${id}")
      # efibootmgr separates the label from the device path with a tab. An
      # entry printed without one keeps its whole line as the label.
      labels[idx]="${rest%%"${tab}"*}"
      details[idx]="${rest}"
    fi
  done <<<"${out}"
}

is_ours() {
  local idx=$((16#$1)) re
  re="hd\\([0-9]+,gpt,${esp_uuid},"
  [[ "$(lower "${details[idx]:-}")" =~ ${re} ]]
}

is_network() {
  local idx=$((16#$1))
  case "$(upper "${labels[idx]:-}")" in
    *HTTP* | *PXE*) return 0 ;;
  esac
  return 1
}

is_shell() {
  local idx=$((16#$1))
  case "$(upper "${labels[idx]:-}")" in
    *SHELL*) return 0 ;;
  esac
  return 1
}

# find_ours sets ours to this disk's entries: those in BootOrder, in that
# order, then any the firmware left out of BootOrder.
find_ours() {
  local id
  ours=()
  for id in ${order[@]+"${order[@]}"}; do
    if is_ours "${id}"; then
      ours+=("${id}")
    fi
  done
  for id in ${entries[@]+"${entries[@]}"}; do
    if is_ours "${id}" && ! in_list "${id}" ${order[@]+"${order[@]}"}; then
      ours+=("${id}")
    fi
  done
}

# esp_loader prints the ESP path of Ubuntu's loader for this architecture, in
# the backslash form a boot entry takes.
esp_loader() {
  local arch candidate
  case "${MACHINE}" in
    aarch64 | arm64) arch=aa64 ;;
    x86_64 | amd64) arch=x64 ;;
    *) return 1 ;;
  esac
  for candidate in "EFI/ubuntu/shim${arch}.efi" "EFI/ubuntu/grub${arch}.efi" "EFI/BOOT/BOOT$(upper "${arch}").EFI"; do
    if [[ -f "${ESP_MOUNT}/${candidate}" ]]; then
      printf '\\%s' "${candidate//\//\\}"
      return 0
    fi
  done
  return 1
}

if [[ ! -d "${EFIVARS_DIR}" ]]; then
  log "no UEFI variables at ${EFIVARS_DIR}; nothing to do"
  exit 0
fi
if ! command -v efibootmgr >/dev/null 2>&1; then
  log "efibootmgr is not installed" >&2
  exit 1
fi

esp_dev="$(findmnt -n -o SOURCE -M "${ESP_MOUNT}" 2>/dev/null || true)"
esp_uuid=""
if [[ -n "${esp_dev}" ]]; then
  esp_uuid="$(lower "$(lsblk -n -o PARTUUID "${esp_dev}" 2>/dev/null || true)")"
  esp_uuid="${esp_uuid//[[:space:]]/}"
fi
if [[ -z "${esp_uuid}" ]]; then
  log "WARN: no GPT partition UUID for an ESP mounted at ${ESP_MOUNT}; leaving BootOrder unchanged"
  exit 0
fi

read_entries
if [[ ${#order[@]} -eq 0 ]]; then
  log "BootOrder is empty; nothing to do"
  exit 0
fi
# The order before any entry is created: a new entry is prepended to BootOrder,
# and must not end the leading network block.
original_order=("${order[@]}")
find_ours

if [[ ${#ours[@]} -eq 0 ]]; then
  disk="$(lsblk -n -o PKNAME "${esp_dev}" 2>/dev/null || true)"
  disk="${disk//[[:space:]]/}"
  partition="$(lsblk -n -o PARTN "${esp_dev}" 2>/dev/null || true)"
  partition="${partition//[[:space:]]/}"
  if ! loader="$(esp_loader)"; then
    log "WARN: no Ubuntu loader for ${MACHINE} on the ESP at ${ESP_MOUNT}; leaving BootOrder unchanged"
    exit 0
  fi
  if [[ -z "${disk}" || -z "${partition}" ]]; then
    log "WARN: cannot find the disk and partition number of ${esp_dev}; leaving BootOrder unchanged"
    exit 0
  fi
  if [[ "${DRY_RUN}" == 1 ]]; then
    log "DRY-RUN: would create boot entry 'ubuntu' for /dev/${disk} partition ${partition} -> ${loader}"
    exit 0
  fi
  if ! efibootmgr -c -d "/dev/${disk}" -p "${partition}" -L ubuntu -l "${loader}" >/dev/null; then
    log "WARN: could not create a boot entry for ${esp_dev}; leaving BootOrder unchanged"
    exit 0
  fi
  log "created boot entry 'ubuntu' for /dev/${disk} partition ${partition} -> ${loader}"
  read_entries
  find_ours
  if [[ ${#ours[@]} -eq 0 ]]; then
    log "WARN: the new boot entry does not name the ESP's partition UUID ${esp_uuid}; leaving BootOrder unchanged"
    exit 0
  fi
fi

network=()
rest=()
shell=()
leading=1
for id in "${original_order[@]}"; do
  if in_list "${id}" "${ours[@]}"; then
    leading=0
    continue
  fi
  if [[ ${leading} -eq 1 ]] && is_network "${id}"; then
    network+=("${id}")
    continue
  fi
  leading=0
  if is_shell "${id}"; then
    shell+=("${id}")
  else
    rest+=("${id}")
  fi
done

current="$(join_ids "${order[@]}")"
updated="$(join_ids ${network[@]+"${network[@]}"} "${ours[@]}" ${rest[@]+"${rest[@]}"} ${shell[@]+"${shell[@]}"})"
summary="this disk: $(join_ids "${ours[@]}"), EFI shell last: $(join_ids ${shell[@]+"${shell[@]}"})"
if [[ "${updated}" == "${current}" ]]; then
  log "BootOrder is already ${current} (${summary})"
  exit 0
fi
if [[ "${DRY_RUN}" == 1 ]]; then
  log "DRY-RUN: BootOrder ${current} -> ${updated} (${summary})"
  exit 0
fi
efibootmgr -o "${updated}" >/dev/null
log "BootOrder ${current} -> ${updated} (${summary})"
