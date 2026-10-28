# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# The Calico stream the agent installs by default (defaultCalicoCniStream in
# agent/internal/agent/cni.go). Bump here and run `make lock`; see README.md.
CALICO_VERSIONS ?= v3.30.1

CNI := cni-calico
PRODUCT := Calico
VERSIONS := $(foreach v,$(CALICO_VERSIONS),v$(patsubst v%,%,$(v)))
CONTENT_SPEC := $(CURDIR)/packaging/nfpm-manifests.yaml
CONTENT_KIND := manifests

# local.mk sets CNI_DOCKERHUB_MIRROR so crane reads docker.io images from Harbor.
-include local.mk
export CNI_DOCKERHUB_MIRROR

include ../cni-common/deb.mk
