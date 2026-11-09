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
laptop$ kubectl apply -f https://github.com/eventide-oss/vesper/releases/download/v0.1.0-rc.1/install.yaml
laptop$ kubectl -n vesper-system rollout status deploy/vesper-controller-manager
laptop$ kubectl -n vesper-system logs deploy/vesper-controller-manager | head -1   # Starting Vesper {"version": "v0.1.0-rc.1"}
```

Install `vesperctl` from the same release, after checking the archive (here
for macOS on Apple silicon; pick the archive of your platform):

```sh
laptop$ gh release download v0.1.0-rc.1 --repo eventide-oss/vesper \
  --pattern 'vesperctl_*_darwin_arm64.tar.gz' --pattern checksums.txt
laptop$ shasum -a 256 --check --ignore-missing checksums.txt        # OK
laptop$ gh attestation verify vesperctl_0.1.0-rc.1_darwin_arm64.tar.gz --repo eventide-oss/vesper
laptop$ tar xzf vesperctl_0.1.0-rc.1_darwin_arm64.tar.gz vesperctl
laptop$ sudo install -m 755 vesperctl /usr/local/bin/vesperctl
laptop$ vesperctl version                                            # v0.1.0-rc.1
```

Give the operator the domain of previews, by adding an argument to its
Deployment:

```sh
laptop$ kubectl -n vesper-system patch deployment vesper-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--preview-domain=preview.example.com"}]'
laptop$ kubectl -n vesper-system rollout status deploy/vesper-controller-manager
```

Applying `install.yaml` again, to upgrade for example, resets the arguments:
add the argument again after it.

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

Change the image: the new version rolls out, and the old pod stops only once
the new one runs.

```sh
laptop$ vesperctl create demo --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 2h
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
