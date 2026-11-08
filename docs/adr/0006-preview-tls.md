# ADR-0006: Serve previews over HTTPS with the default certificate of the ingress controller

- **Status:** Accepted
- **Date:** 2026-11-08
- **Deciders:** @damnthonyy
- **Issue / PR:** #100

## Context

With a preview domain, each preview gets a public address,
`<name>-<namespace>-<hash>.<domain>`, through an Ingress (#45). People open it
over the Internet, so it needs HTTPS: browsers flag plain HTTP, and features
such as `Secure` cookies and service workers only work over HTTPS.

The ingress controller of the cluster (Traefik, HAProxy...) terminates TLS: it
holds the certificate and its private key, decrypts the traffic and passes it
to the preview. An Ingress can name a certificate only through a Secret in its
own namespace, and each preview has a namespace of its own (ADR-0002).
Previews come and go many times a day.

One certificate can cover every preview: a wildcard for `*.<domain>`, since
the host of a preview is a single label of the preview domain. Let's Encrypt
issues wildcards through the DNS-01 challenge only, which cert-manager can
answer with the API of the DNS provider.

## Decision

The operator flag `--preview-tls` serves previews over HTTPS with **the
default certificate of the ingress controller**: an administrator configures a
wildcard certificate for `*.<domain>` once, as that default, and the operator
adds a TLS section that names the host and no Secret to each Ingress, and
writes `https://` to `status.url`. Vesper never reads or copies a certificate.

## Alternatives considered

**Copy the wildcard Secret into each preview namespace**, and name it in the
TLS section. It works with every ingress controller. But the operator would
need to read Secrets and to write them in every namespace, a right far beyond
what it has; and the private key of the whole preview domain would sit in
every preview namespace, next to code from any pull request, readable by
anyone allowed to read Secrets there. A leak would expose every preview.

**One certificate per preview**, issued by cert-manager with the HTTP-01
challenge through an annotation on each Ingress. No wildcard and no access to
the DNS API are needed. But Let's Encrypt allows 50 certificates per
registered domain per week, which ephemeral previews would exhaust; each new
preview would wait for its certificate; and the HTTP-01 solver pod would run
in the preview namespace, where the NetworkPolicy, the quota and the
`restricted` profile of Vesper get in its way. The certificate and its key
would also live in the preview namespace, though not the key of the whole
domain.

**The Gateway API**, with the wildcard certificate on one listener of a shared
Gateway. It is the long-term model, but it needs a controller that supports it
and a different object per preview (#99). The flag can keep its meaning then.

## Consequences

- No private key in preview namespaces, and no new right for the operator.
- The setup depends on the ingress controller: it must serve a default
  certificate for hosts whose TLS section names no Secret. With Traefik, set
  the default certificate (the `default` TLS store) and enable TLS on the
  `websecure` entrypoint, as its Helm chart and k3s do by default: Traefik
  enables TLS on a route from the entrypoint, not from the TLS section of the
  Ingress. `docs/development.md` describes it; other controllers have an
  equivalent setting.
- One certificate covers every preview, and its renewal is the job of
  cert-manager (or of whoever issued it), not of Vesper. The server guide
  (#96) describes cert-manager with DNS-01.
- Redirecting `http://` to `https://` is a setting of the ingress controller
  too.
- If a controller without a default certificate must be supported, the
  copied Secret can be revisited, with its risks written down.
