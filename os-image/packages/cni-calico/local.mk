# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Local crane transport for the Calico images deb.
# docker.io/calico/<name> is fetched as <mirror>/calico/<name>.
# quay.io/tigera/operator is left as-is.
#
# The path after the host is the Harbor project that proxies Docker Hub.
# Change it if that project is not nvke.

CNI_DOCKERHUB_MIRROR ?= harbor.cbr01.in.firmus.co/dockerhub
