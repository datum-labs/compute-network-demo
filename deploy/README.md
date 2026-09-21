# Deploying the demo to a Datum project

Everything here is a template. Placeholders look like `<PROJECT>`, and every one
of them is called out with a `FILL IN` comment next to the line that needs it.
Nothing in this directory carries a value that only works in one environment.

You need a Datum project, `kubectl`, and
[`datumctl`](https://github.com/datum-cloud/datumctl) to get a kubeconfig for
it. Two control planes are involved: your **project**, which holds the network
and the workload, and your **organization**, which holds access policy. Only
live mode touches the organization.

```sh
datumctl auth update-kubeconfig --kubeconfig ./project.kubeconfig --project <PROJECT>
export KUBECONFIG=$PWD/project.kubeconfig
```

If you have more than one Datum environment, check which server you are
pointing at before you apply anything — context names are not unique across
environments.

## Simulate mode, which is the one to start with

Three files, in order. Simulate mode needs no credentials, no API access and no
identity, so this stands the demo up on its own.

```sh
kubectl apply -f 00-network.yaml
kubectl apply -f 10-workload.yaml
kubectl apply -f 20-ingress.yaml
```

Watch the Instances come up, one per placement:

```sh
kubectl get instances -l compute.datumapis.com/workload-name=global-mesh -w
```

Then read the hostname the proxy was given and open it:

```sh
kubectl get httpproxy global-mesh -o jsonpath='{.status.hostnames[0]}'
```

TLS provisioning takes a minute or two after the HTTPProxy is first created.

Before you apply `10-workload.yaml`, decide two things:

- **Which locations.** There is one placement per city, selected by city code.
  `kubectl get locations` lists what your project can use. Delete the
  placements you cannot fill; adding one is how you grow the mesh later.
- **Which image tag.** `:main` follows the default branch and will change under
  you. For anything you plan to leave running, pin a release tag or a commit
  SHA. See "Publishing the image" in the repository README.

The published image is public, so no image pull secret is needed. The
commented-out `imagePullSecrets` stanza is there for the case where you publish
your own build somewhere private.

## Live mode

In live mode each Instance discovers its real peers through the Datum Cloud API
and measures real round-trip times to them. That needs an identity, a
permission grant, and a key mounted into the workload.

```sh
# 1. The service account, on the project control plane.
kubectl apply -f live/00-serviceaccount.yaml

# 2. The permission grant, on the ORGANIZATION control plane. Fill in the two
#    UIDs first; the file says where to read them from. Wait for Ready=True.
datumctl auth update-kubeconfig --kubeconfig ./org.kubeconfig --organization <ORG>
kubectl --kubeconfig ./org.kubeconfig apply -f live/10-policybinding.yaml

# 3. The key, as a Secret. Create the key from the portal or datumctl and
#    download the JSON — the private half is only ever returned once.
kubectl create secret generic global-mesh-credentials \
  --from-file=credentials.json=./global-mesh-key.json

# 4. The workload, in place of the simulate-mode one.
kubectl apply -f live/30-workload.yaml
```

`live/20-credentials-secret.example.yaml` shows the shape of the key file. It is
an example; create the Secret from the downloaded file rather than editing a
private key into a YAML file.

**If the Instances cannot reach the API**, discovery has a fallback: set
`MESH_PEERS` in `live/30-workload.yaml` to the Instances' private addresses and
re-apply. The command that prints them is in the comment beside the variable.
The page shows a small "Static peers" marker when it is running on that
fallback, so it is never silently fibbing about where its view came from.

## The unikernel tier

`unikernel/` is an optional second stack that runs the same demo on the
`unikernel` runtime class, with its own workload name and its own hostname, so
the two can be opened side by side. It needs an image built specially — see
"Run it on the unikernel tier" in the repository README — and it shares the
network from `00-network.yaml`.

## Changing it afterwards

Edit the objects in place. Updating the workload rolls the Instances; deleting
and recreating the HTTPProxy gives you a **new hostname**, which is usually not
what you want if you have shared a link.

To grow the mesh while an audience is watching, add a placement, or raise
`minReplicas` on one that is already there. The page narrates the change on its
own as the new Instances join the network.

## Tearing it down

```sh
kubectl delete -f 20-ingress.yaml -f 10-workload.yaml -f 00-network.yaml
```

Delete the workload before the network; the network cannot be removed while
Instances are still attached to it.
