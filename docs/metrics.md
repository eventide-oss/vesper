# Metrics

The operator serves metrics in the [Prometheus](https://prometheus.io/) text
format on `/metrics`: the metrics of Vesper, and those of controller-runtime,
the library the operator is built on.

## Metrics of Vesper

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `vesper_previews_active` | gauge | `phase` | Preview environments that exist and are not being deleted, by phase: `Pending`, `Ready`, `Failed`, `Expired`, or `Unknown` before the operator first sees them. Every phase is reported, at zero when there is none. |
| `vesper_previews_expired_total` | counter | none | Preview environments the operator deleted because their ttl passed, since it started. |

`vesper_previews_active` is counted from the cache of the operator each time
Prometheus reads the metrics: it is right after a restart, and costs no
request to the API server. If the previews cannot be read, the scrape fails
with the error rather than report wrong values.

`vesper_previews_expired_total` starts at zero when the operator starts, as
every counter does: use `rate()` or `increase()`, which handle the resets. A
preview deleted by a user is not counted.

With several replicas, every replica reports `vesper_previews_active` from its
own cache, so use `max()` rather than `sum()`; only the leader expires
previews and counts them.

No label holds the name of a preview: each value of a label makes a series
that Prometheus keeps, so the labels have a few fixed values only.

## Metrics of controller-runtime

Among others:

| Metric | Meaning |
| --- | --- |
| `controller_runtime_reconcile_total{controller="previewenvironment", result}` | reconciliations, by result: `success`, `error`, `requeue`, `requeue_after` |
| `controller_runtime_reconcile_errors_total{controller="previewenvironment"}` | reconciliations that returned an error |
| `controller_runtime_reconcile_time_seconds{controller="previewenvironment"}` | duration of the reconciliations (histogram) |
| `workqueue_depth{name="previewenvironment"}` | previews waiting to be reconciled |

The [Kubebuilder book](https://book.kubebuilder.io/reference/metrics-reference)
lists them all.

## Example queries

```promql
sum by (phase) (vesper_previews_active)             # previews per phase (one replica)
vesper_previews_active{phase="Failed"} > 0          # an alert: a preview is failing
increase(vesper_previews_expired_total[1d])         # previews expired over a day
rate(controller_runtime_reconcile_errors_total{controller="previewenvironment"}[5m])
```

## Read the metrics

### In a cluster

`make deploy` (`config/default`) serves the metrics over HTTPS on port 8443,
behind the Service `vesper-controller-manager-metrics-service` in
`vesper-system`. Only an authenticated caller allowed to `get` the
non-resource URL `/metrics` can read them: the operator checks the token of
each request with the API server (a TokenReview, then a SubjectAccessReview).
The ClusterRole `vesper-metrics-reader` grants that right.

To read them by hand, with a ServiceAccount of your own:

```sh
kubectl create serviceaccount metrics-check -n default
kubectl create clusterrolebinding metrics-check --clusterrole=vesper-metrics-reader \
  --serviceaccount=default:metrics-check
kubectl port-forward -n vesper-system svc/vesper-controller-manager-metrics-service 8443:8443 &
TOKEN=$(kubectl create token metrics-check -n default)
curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics | grep '^vesper_'
kill %1   # stop the port-forward
kubectl delete clusterrolebinding metrics-check
kubectl delete serviceaccount metrics-check -n default
```

`-k` accepts the self-signed certificate the operator generates when it has no
other. For production, give it a certificate from cert-manager (the
`CERTMANAGER` sections of `config/default/kustomization.yaml`).

With [prometheus-operator](https://prometheus-operator.dev/), enable the
`PROMETHEUS` section of `config/default/kustomization.yaml`: it adds a
ServiceMonitor that tells Prometheus to scrape the Service. Bind
`vesper-metrics-reader` to the ServiceAccount of Prometheus.

### Locally

`make run` serves no metrics. To read them, start the operator with a plain
HTTP endpoint on your machine:

```sh
go run ./cmd --metrics-bind-address=127.0.0.1:8080 --metrics-secure=false
curl -s http://127.0.0.1:8080/metrics | grep '^vesper_'
```
