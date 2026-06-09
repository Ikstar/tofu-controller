# Branch planner development

## How to run this

You can run this with

    go run ./cmd/branch-planner/

but it won't do much without a configuration. The configuration is in
the form of a ConfigMap in your Kubernetes cluster (as accessed by
`current-config` in your kubeconfig).

# Set up a Terraform and GitRepository

You can use my "helloworld" repository itself for this for now, since
it is public, has a valid Terraform program in it, and has an open PR.

Create a GitRepository object and a Terraform object representing this:

```bash
kubectl apply -f- <<EOF
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: helloworld
  namespace: default
spec:
  interval: 30s
  url: https://github.com/squaremo/tf-controller-helloworld
  ref:
    branch: main
---
apiVersion: infra.contrib.fluxcd.io/v1alpha2
kind: Terraform
metadata:
  name: helloworld-tf
  namespace: default
spec:
  path: ./
  interval: 1m
  sourceRef:
    kind: GitRepository
    name: helloworld
    namespace: default
EOF
```

## Create a suitable secret

The secret needs to contain a field "token" with a personal access
token. It needs "read" permission to the repository or repositories in
question, and a [fine-grained
token](https://github.com/settings/tokens?type=beta) will work for
that. I used "Public read-only" rather than specifying individual
repos.

Assuming you have put the token in an environment variable `GITHUB_TOKEN`:

```bash
kubectl create secret generic branch-planner-token -n default --from-literal=token=$GITHUB_TOKEN
```

## Create a config

The configuration given in a ConfigMap in a form specified in
[internal/server/polling/config.go][].

Note the `resources` field is a string value (`|` in the example below
indicates a multiline string), with internal structure.

This will create a `ConfigMap` that works with the `GitRepository` and
`Terraform` object above:

```bash
kubectl apply -f- <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: branch-planner
  namespace: default
data:
  secretName: branch-planner-token
  resources: |
    - namespace: default
      name: helloworld-tf
EOF
```

### Filtering pull requests by path

When a `Terraform` object sets `spec.branchPlanner.enablePathScope: true`, the
branch planner only acts on a pull request if at least one changed file matches
**any** of:

1. The `spec.path` prefix (literal `strings.HasPrefix` match — unchanged
   behavior).
2. An optional `additionalPaths` list on the matching `resources:` entry in
   the ConfigMap (applies to every Terraform that entry matches — including
   every Terraform in a wildcard-namespace entry).
3. An optional `spec.branchPlanner.additionalPaths` on the `Terraform` object
   itself (scoped to that one resource).

Entries in either `additionalPaths` list are evaluated as
[doublestar](https://github.com/bmatcuk/doublestar) globs — `**`, `*`, `?`, and
character classes are supported. Patterns are matched against PR file paths as
returned by the git provider (no leading `./`), and entries can be literal
paths or globs. Invalid patterns are logged and skipped (they will never match)
so a single bad glob never disables the planner.

Both `additionalPaths` lists are ignored when `enablePathScope: false`.

ConfigMap example combining the two scopes:

```yaml
data:
  secretName: branch-planner-token
  resources: |
    - namespace: flux-system
      name: open-tofu-global
      # Global to this Terraform (or, for a wildcard namespace entry without
      # `name:`, every Terraform in the namespace).
      additionalPaths:
        - infra/tenant-infrastructure-configs.yaml
        - infra/terraform/modules/**
```

Terraform CR example combining `enablePathScope` with per-object globs:

```yaml
apiVersion: infra.contrib.fluxcd.io/v1alpha2
kind: Terraform
metadata:
  name: open-tofu-tenant-a
  namespace: flux-system
spec:
  path: ./infra/terraform/environments/tenant-a
  branchPlanner:
    enablePathScope: true
    additionalPaths:
      - infra/tenant-a/**
```

### Targeting a different Kubernetes cluster

Supply the env entry `KUBECONFIG` to use a different kubeconfig; it
will still use `current-config`, but you can arrange for that to point
to the intended cluster.
