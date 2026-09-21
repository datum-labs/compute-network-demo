# Publishing the images

## The container image

`.github/workflows/publish.yaml` builds and pushes to
`ghcr.io/datum-labs/compute-network-demo` using the workflow's built-in token,
on pushes to `main`, on `v*` tags, and on demand.

| Tag | When | Moves? |
| --- | --- | --- |
| `sha-<12 chars>` | every publish | no |
| `main` | pushes to the default branch | yes |
| `v1.2.3` | a `v*` tag or a published release | no |
| `latest` | a `v*` tag or a published release | yes |

The deploy manifests default to `:main` so that a fresh clone works. Pin a
`sha-` or `v*` tag for anything you intend to leave running.

Two things about this build are deliberate.

**No provenance, no SBOM, and Docker media types rather than OCI ones.**
Attestations turn the pushed artifact into an OCI image index, and the Datum
runtime that pulls this image selects a plain
`application/vnd.docker.distribution.manifest.v2+json`. That is why the
workflow does not use the shared organization publish workflow, which attaches
provenance. In `docker buildx` terms: `--provenance=false --sbom=false` plus
`--output type=image,push=true,oci-mediatypes=false`, because media types are
an exporter option rather than a build flag.

**linux/amd64 only.** A multi-platform push is a manifest list by definition,
which runs into the same thing, and there is no arm64 instance type to run it
on. The Dockerfile is architecture-neutral, so building another platform by
hand works if that changes.

### If the package ever comes out private

The package published here is public and pulls anonymously, which is why the
deploy manifests carry no image pull secret. Do not assume that of the next
one: a package created by a workflow usually inherits its visibility from the
repository, but a previous push under this organization landed as a private
package even though the repository was public, and the REST API for changing
package visibility returned 404 for it. If
`docker pull ghcr.io/datum-labs/compute-network-demo:main` ever fails
anonymously, fix it once by hand:

> GitHub → the `datum-labs` organization → **Packages** →
> `compute-network-demo` → **Package settings** → **Change visibility** →
> Public.

While you are there, "Manage Actions access" should list this repository with
Write, which the workflow needs and which is set up automatically on the first
push.

## The unikernel image, which is built by hand

The demo also runs unmodified on the `unikernel` runtime class, which boots the
binary as a unikernel instead of inside a VM with a kernel and a userland. Both
tiers serve the same page, so they can be shown side by side.

CI does not build that image, because it is not an ordinary container image.
The unikernel tier needs an OCI image whose platform is `kraftcloud/x86_64` and
whose single layer carries an EROFS root filesystem. Producing one needs the
`datumctl` compute plugin locally:

```sh
datumctl compute build --analyze --push \
  --output <REGISTRY>/<NAMESPACE>/compute-network-demo-unikernel:<TAG> .
```

`build` uses `Dockerfile.datum` when it is present, which is why the unikernel
image is defined there rather than in `Dockerfile`. That file differs from the
general-purpose one in two ways the packager cares about. It ends at `scratch`,
because the root filesystem is held in guest RAM and every unused file costs
memory at boot. And it leaves `WORKDIR` at `/`, because the packager emulates a
non-root `WORKDIR` by wrapping the entrypoint in a shell, and a scratch image
has no shell to wrap it with. The distroless `:nonroot` bases set
`WORKDIR /home/nonroot`, which is why they cannot be packaged as they are.

The binary itself needs no special linking — it is the same `CGO_ENABLED=0`
static build the container image uses, and `--analyze` confirms the entrypoint
before packaging. Then point `deploy/unikernel/10-workload.yaml` at the result.

Teaching CI to do this would mean running the compute plugin and its packager
on a hosted runner, which the toolchain does not support today.
