#!/usr/bin/env bash
# Adds all checker-specific Go dependencies.
# Run once after cloning: bash scripts/setup.sh
set -euo pipefail

echo "→ Deprecated APIs: Pluto"
go get github.com/fairwindsops/pluto/v5@latest

echo "→ Helm / CVEs: Nova"
go get github.com/fairwindsops/nova@latest

echo "→ CRD schemas: kubeconform"
go get github.com/yannh/kubeconform@latest

echo "→ AWS SDK v2 (EKS Insights, IAM)"
go get github.com/aws/aws-sdk-go-v2@latest
go get github.com/aws/aws-sdk-go-v2/config@latest
go get github.com/aws/aws-sdk-go-v2/service/eks@latest
go get github.com/aws/aws-sdk-go-v2/service/iam@latest

echo "→ etcd client v3"
go get go.etcd.io/etcd/client/v3@latest

echo "→ SQLite (RAG vector store)"
go get github.com/mattn/go-sqlite3@latest

echo "→ Tidying modules..."
go mod tidy

echo "✓ All dependencies installed. Run: make build"
