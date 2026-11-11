# Try Vesper on a server

This guide runs Vesper on a real server reachable from the Internet: a VPS
with [k3s](https://k3s.io/), a wildcard DNS record, and previews opened from a
browser. It installs the published release, as a user would.

Values to replace:

| Placeholder | Meaning |
| --- | --- |
| `<VPS_IP>` | the public IPv4 address of the server |
| `preview.example.com` | the domain of previews; each preview gets `<name>-<namespace>-<hash>.preview.example.com` |
| `vps` | the SSH alias of the server, set in `~/.ssh/config` |

Commands run either on your machine (`laptop$`) or on the server
(`server$`).

## Before you start

- A VPS with 2 vCPU and 4 GB of RAM or more, running Ubuntu 24.04, that you
  reach over SSH with a key, as a user with `sudo`. Password logins and root
  logins are disabled.
- A domain whose DNS you manage.
- `kubectl` on your machine.
- A network that lets SSH out: some school or company networks block port 22.

## Security

- **Only ports 22, 80 and 443 are open.** The Kubernetes API (6443) and the
  kubelet (10250) are never exposed: you reach the API through an SSH tunnel.
- The kubeconfig of k3s gives full admin access to the cluster: keep it on
  your machine, with mode `600`, never in a repository.
- A namespace is a soft boundary. Vesper isolates previews from each other,
  but does not promise hard multi-tenant isolation: do not run previews of
  untrusted code on a server that hosts anything sensitive.

## 1. Install k3s

**Firewall first.** In the firewall of your provider, allow TCP 22, 80 and
443, then add a last rule that drops everything else, and check that the
firewall is applied to the server. Without that last rule, some providers
allow every port: nothing shows it until k3s opens 6443.

Download the install script, and read it before you run it:

```sh
server$ curl -sfL https://get.k3s.io -o k3s-install.sh
server$ head -20 k3s-install.sh
```

Install k3s, pinned to a version, with Secrets encrypted on disk:

```sh
server$ INSTALL_K3S_VERSION=v1.37.2+k3s1 sh k3s-install.sh --secrets-encryption
```

`INSTALL_K3S_VERSION` pins the version, so that two installs are the same;
v1.37 is the Kubernetes version Vesper is tested with. `--secrets-encryption`
encrypts Secrets in the datastore of k3s. The script asks for your `sudo`
password and starts k3s as a service.

Check the node and the components of k3s:

```sh
server$ sudo k3s kubectl get nodes        # STATUS Ready, VERSION v1.37.2+k3s1
server$ sudo k3s kubectl get pods -A      # coredns, traefik, svclb-traefik... Running
```

k3s comes with Traefik, the ingress controller that receives the traffic of
previews on ports 80 and 443, and with a NetworkPolicy controller, so the
network isolation of previews is enforced.

From your machine, check that only the expected ports answer:

```sh
laptop$ nc -vz -G 5 <VPS_IP> 22       # succeeded
laptop$ nc -vz -G 5 <VPS_IP> 6443     # timed out: the Kubernetes API is closed
laptop$ nc -vz -G 5 <VPS_IP> 10250    # timed out: the kubelet is closed
```

(`-G` sets the timeout on macOS; on Linux, use `-w 5`.)

## 2. Reach the cluster from your machine

The kubeconfig of k3s, `/etc/rancher/k3s/k3s.yaml`, is readable by root only.
Copy it to your home directory with your user as owner, fetch it, and delete
the copy on the server:

```sh
server$ sudo install -m 600 -o "$USER" -g "$USER" /etc/rancher/k3s/k3s.yaml ~/k3s.yaml
laptop$ mkdir -p ~/.kube
laptop$ scp vps:k3s.yaml ~/.kube/vps.yaml
laptop$ chmod 600 ~/.kube/vps.yaml
laptop$ ssh vps rm k3s.yaml
```

It points to `https://127.0.0.1:6443`. Open an SSH tunnel that forwards that
port of your machine to the same port of the server, in a terminal of its
own; it runs until you stop it with Ctrl-C:

```sh
laptop$ ssh -N -L 6443:127.0.0.1:6443 vps
```

In another terminal, use that kubeconfig for this shell only, so that your
other clusters are not affected:

```sh
laptop$ export KUBECONFIG=~/.kube/vps.yaml
laptop$ kubectl get nodes        # the node of the server, Ready
```

Every `kubectl` and `vesperctl` command below runs in that terminal, with the
tunnel open.

## 3. Install Vesper

Install the operator from the release:

```sh
laptop$ kubectl apply -f https://github.com/eventide-oss/vesper/releases/download/v0.1.0/install.yaml
laptop$ kubectl -n vesper-system rollout status deploy/vesper-controller-manager
laptop$ kubectl -n vesper-system logs deploy/vesper-controller-manager | head -1   # Starting Vesper {"version": "v0.1.0"}
```

Install `vesperctl` from the same release, after checking the archive (here
for macOS on Apple silicon; pick the archive of your platform):

```sh
laptop$ gh release download v0.1.0 --repo eventide-oss/vesper \
  --pattern 'vesperctl_*_darwin_arm64.tar.gz' --pattern checksums.txt
laptop$ shasum -a 256 --check --ignore-missing checksums.txt        # OK
laptop$ gh attestation verify vesperctl_0.1.0_darwin_arm64.tar.gz --repo eventide-oss/vesper
laptop$ tar xzf vesperctl_0.1.0_darwin_arm64.tar.gz vesperctl
laptop$ sudo install -m 755 vesperctl /usr/local/bin/vesperctl
laptop$ vesperctl version                                            # v0.1.0
```

Give the operator the domain of previews, by adding an argument to its
Deployment:

```sh
laptop$ kubectl -n vesper-system patch deployment vesper-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--preview-domain=preview.example.com"}]'
laptop$ kubectl -n vesper-system rollout status deploy/vesper-controller-manager
```

Applying `install.yaml` again, to upgrade for example, resets the arguments:
add the argument again after it, or keep the flags in a kustomize overlay, as
the [quickstart](quickstart.md#configure-the-operator) describes.

## 4. Open a preview from a browser

At your DNS provider, point every name under the preview domain to the
server with one wildcard record:

| Type | Name | Value |
| --- | --- | --- |
| `A` | `*.preview` | `<VPS_IP>` |

If the DNS provider can proxy traffic (the orange cloud of Cloudflare), turn
it off for this record: the server must answer the requests itself, and
present its own certificate in step 7. Check it from your machine:

```sh
laptop$ dig +short test.preview.example.com @1.1.1.1   # <VPS_IP>
```

Create a preview, and follow it until it is Ready:

```sh
laptop$ vesperctl create demo --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 2h
laptop$ vesperctl get demo      # Phase: Ready, URL: http://demo-default-<hash>.preview.example.com
```

Open the URL in a browser, from your machine and from a phone on its mobile
network: both show the nginx welcome page. Some mobile browsers refuse plain
HTTP by default; step 7 serves previews over HTTPS.

## 5. Run the lifecycle

Change the image of `demo`: the new version rolls out, and the old pod stops
only once the new one runs.

```sh
laptop$ kubectl patch pe demo --type=merge -p '{"spec":{"image":"nginxinc/nginx-unprivileged:1.28-alpine"}}'
laptop$ curl -sI http://demo-default-<hash>.preview.example.com | grep -i '^server'   # nginx/1.28.x
```

Let a preview expire: with a TTL of two minutes, the preview and its
namespace are gone a few seconds after the TTL.

```sh
laptop$ vesperctl create short --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 2m
laptop$ sleep 150; vesperctl get short          # not found
laptop$ kubectl get ns | grep short             # nothing
```

Delete a preview: its namespace goes first, then the preview, and its URL
answers 404.

```sh
laptop$ vesperctl delete demo
laptop$ kubectl get ns | grep demo              # nothing, after a few seconds
```

Invalid previews are refused by the API server, before the operator sees
them:

```sh
laptop$ vesperctl create bad1 --image nginxinc/nginx-unprivileged:latest --port 8080   # refused: not pinned
laptop$ vesperctl create bad2 --image nginxinc/nginx-unprivileged:1.27-alpine --ttl 30s  # refused: under 1m
```

## 6. Check the isolation of previews

Create two previews, and keep the names of their namespaces:

```sh
laptop$ vesperctl create alice --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1h
laptop$ vesperctl create bob --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1h
laptop$ A=$(kubectl get pe alice -o jsonpath='{.status.namespace}')
laptop$ B=$(kubectl get pe bob -o jsonpath='{.status.namespace}')
laptop$ IP=<VPS_IP>
```

From the pod of `alice`, try to reach the other preview, the Kubernetes API,
the kubelet through the public address of the server, and the internet:

```sh
laptop$ kubectl exec -n "$A" deploy/app -- wget -qO- -T 3 "http://app.$B"                     # refused
laptop$ kubectl exec -n "$A" deploy/app -- wget -qO- -T 3 https://kubernetes.default           # refused
laptop$ kubectl exec -n "$A" deploy/app -- wget -qO- -T 3 "http://$IP:10250"                  # refused
laptop$ kubectl exec -n "$A" deploy/app -- wget -qO- -T 3 http://example.com >/dev/null; echo $?   # 0
```

A refused connection or a timeout means the NetworkPolicy blocked it. An HTTP
error such as 401 or 400 means the connection got through: before
v0.1.0-rc.2, the API server and the kubelet were reached through the public
address of the node ([GHSA-m437-fj8p-6x8h](https://github.com/eventide-oss/vesper/security/advisories/GHSA-m437-fj8p-6x8h)).

Check the bounds of a preview. The API server refuses more than 5 replicas;
the ResourceQuota of the preview namespace refuses any pod over its budget,
even one created by hand:

```sh
laptop$ vesperctl create big --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --replicas 6   # refused: at most 5
laptop$ vesperctl create big --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --replicas 5
laptop$ G=$(kubectl get pe big -o jsonpath='{.status.namespace}')
laptop$ kubectl describe resourcequota -n "$G"     # pods 5/10, limits.memory 2560Mi/4Gi, no NodePort or LoadBalancer
laptop$ kubectl apply -n "$G" -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: over-budget
spec:
  containers:
  - name: app
    image: nginxinc/nginx-unprivileged:1.27-alpine
    resources:
      limits: {memory: 5Gi, cpu: 500m}
    securityContext:
      allowPrivilegeEscalation: false
      runAsNonRoot: true
      capabilities: {drop: ["ALL"]}
      seccompProfile: {type: RuntimeDefault}
YAML
# Error from server (Forbidden): ... exceeded quota: preview, requested: limits.memory=5Gi ...
laptop$ vesperctl delete big --yes
```

Read the metrics, as [Metrics](metrics.md) describes: without a token the
endpoint answers 401, with the token of a ServiceAccount bound to
`vesper-metrics-reader` it serves `vesper_previews_active` and
`vesper_previews_expired_total`. The counter starts at zero each time the
operator starts.

## 7. Serve previews over HTTPS

Vesper serves previews over HTTPS with the default certificate of the ingress
controller ([ADR-0006](adr/0006-preview-tls.md)): one wildcard certificate for
`*.preview.example.com`, from Let's Encrypt. Let's Encrypt issues wildcards
only through the DNS-01 challenge: [cert-manager](https://cert-manager.io/)
proves that you control the domain by writing a TXT record through the API of
your DNS provider, and renews the certificate before it expires. This step
uses Cloudflare; cert-manager supports other providers.

**Create an API token** at your DNS provider. With Cloudflare: permissions
Zone · DNS · Edit and Zone · Zone · Read, restricted to the zone of your
domain, and, if you can, to the address of the server. The token is shown
once: keep it in a password manager.

Install cert-manager, pinned:

```sh
laptop$ kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.21.3/cert-manager.yaml
laptop$ kubectl -n cert-manager rollout status deploy/cert-manager-webhook
```

Store the token in a Secret without showing it or writing it to a file
(`read -s` reads without echo; the syntax is the one of zsh, use
`read -rs CF_TOKEN` in bash):

```sh
laptop$ read -s "CF_TOKEN?Cloudflare token: "
laptop$ printf %s "$CF_TOKEN" | kubectl create secret generic cloudflare-api-token -n cert-manager --from-file=api-token=/dev/stdin
laptop$ unset CF_TOKEN
```

Try the staging environment of Let's Encrypt first: its certificates are not
trusted by browsers, but it does not apply the rate limits of production, so
mistakes cost nothing.

```sh
laptop$ kubectl apply -f - <<'EOF2'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-staging
spec:
  acme:
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: letsencrypt-staging-account
    solvers:
    - dns01:
        cloudflare:
          apiTokenSecretRef:
            name: cloudflare-api-token
            key: api-token
EOF2
laptop$ kubectl get clusterissuer letsencrypt-staging      # READY True
```

Ask for the wildcard certificate, in the namespace of Traefik
(`kube-system` in k3s), and make it the default certificate of Traefik:

```sh
laptop$ kubectl apply -f - <<'EOF2'
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-preview
  namespace: kube-system
spec:
  secretName: wildcard-preview-tls
  dnsNames:
  - "*.preview.example.com"
  issuerRef:
    kind: ClusterIssuer
    name: letsencrypt-staging
---
apiVersion: traefik.io/v1alpha1
kind: TLSStore
metadata:
  name: default
  namespace: kube-system
spec:
  defaultCertificate:
    secretName: wildcard-preview-tls
EOF2
laptop$ kubectl get certificate -n kube-system -w      # READY True after one to three minutes
laptop$ echo | openssl s_client -connect test.preview.example.com:443 -servername test.preview.example.com 2>/dev/null \
  | openssl x509 -noout -subject -issuer               # CN=*.preview.example.com, issuer (STAGING) ...
```

k3s configures Traefik with TLS on its `websecure` entrypoint, which Vesper
needs (see [HTTPS](development.md#https)); with another Traefik install, set
it as described there.

Then switch to production: create the same ClusterIssuer named
`letsencrypt-prod` with the server
`https://acme-v02.api.letsencrypt.org/directory` and the account Secret
`letsencrypt-prod-account`, and point the certificate to it:

```sh
laptop$ kubectl patch certificate wildcard-preview -n kube-system --type=merge \
  -p '{"spec":{"issuerRef":{"name":"letsencrypt-prod"}}}'
laptop$ kubectl get certificate -n kube-system -w      # READY True again
```

The issuer is now `Let's Encrypt`, without `(STAGING)`. Turn on HTTPS in the
operator, and open a preview:

```sh
laptop$ kubectl -n vesper-system patch deployment vesper-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--preview-tls"}]'
laptop$ vesperctl create secure --image nginxinc/nginx-unprivileged:1.28-alpine --port 8080 --ttl 2h
laptop$ curl -sI "$(kubectl get pe secure -o jsonpath='{.status.url}')" | head -1    # HTTP/2 200
laptop$ kubectl get secrets -n "$(kubectl get pe secure -o jsonpath='{.status.namespace}')"   # No resources found
```

Browsers show the padlock. The preview namespace holds no Secret: the
certificate and its key stay with Traefik.

## 8. Clean up

Delete the previews you created. To remove everything:

```sh
laptop$ kubectl delete -f https://github.com/eventide-oss/vesper/releases/download/v0.1.0/install.yaml
laptop$ kubectl delete certificate wildcard-preview -n kube-system
laptop$ kubectl delete tlsstore default -n kube-system
laptop$ kubectl delete -f https://github.com/cert-manager/cert-manager/releases/download/v1.21.3/cert-manager.yaml
server$ /usr/local/bin/k3s-uninstall.sh
```

Then remove the `*.preview` DNS record, revoke the API token at your DNS
provider, and delete `~/.kube/vps.yaml`.
