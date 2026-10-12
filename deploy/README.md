# Deploy a shared ownership workspace

The hosted mode adds GitHub login, multiple managed repositories, saved analysis
links, manual/scheduled/push-triggered refresh, and UI administration. The local
`ghca explore /path/to/repo` command remains unchanged.

## Access model

This is a trusted internal workspace. Every authorized member can read **all
imported repositories and source code**, regardless of their individual GitHub
repository permissions. Import only repositories you want to share with that
workspace. Administrators control imports, schedules, archiving and other admins.

With the example configuration, users must be active members of GitHub's
`conduktor` organization **and** have a verified `@conduktor.io` email on their
GitHub account (it can be private or secondary). `sderosiaux` is the initial
administrator. Additional admins can be added in Settings; they must satisfy the
same sign-in policy. Membership and email verification are rechecked during
active sessions at most every ten minutes. Sessions expire after eight hours or
the GitHub token's earlier expiry. Access checks protect HTML and every data API.

## 1. Prepare the VM and DNS

Install Docker Engine with the Compose plugin. Choose a hostname you control,
point its A/AAAA records at the VM, and allow inbound TCP 80/443. Keep SSH available
for administration. If another reverse proxy already owns these ports, connect
atlas to that proxy instead of starting a second Caddy instance. Forward the
original Host header and route `/webhooks/github` as well as the application.

Clone **this application's public repository** onto the VM. Managed private
repositories are fetched into a private Docker volume, never into the public
source repository or image.

```sh
git clone https://github.com/sderosiaux/git-contributor-insights.git
cd git-contributor-insights
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
```

Set `ATLAS_HOST` and `ATLAS_PUBLIC_URL` in `deploy/.env` to your actual HTTPS
hostname, without a trailing slash. Set organization, email domain, initial
admins, worker count and memory budget for your instance.

## 2. Start the server and connect GitHub

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
docker compose --env-file deploy/.env -f deploy/compose.yaml logs atlas
```

On first startup, the log contains a **one-time setup URL** with a private token.
Open it in your browser. The page shows the organization, initial administrators,
and exact read-only permissions before sending you to GitHub. An organization
owner creates the app through this flow. The generated key and client/webhook
secrets return directly to the server and are stored privately in `atlas_data`;
there are no secrets to copy through a chat or commit to Git.

Choose **Create GitHub App**, then install it on the configured organization with
**Only select repositories**. Select the 2–3 repos you want to analyze. The server
switches to the authenticated workspace automatically after registration. If the
setup is interrupted before registration completes, reopen the original link.
The setup link stops working once the app is configured.

The app requests Contents and Metadata read access, Members read access for
organization checks, Email addresses read access for verified-domain checks, and
push events. Repository content is never written back to GitHub. Installation
tokens handle fetching in the background; user tokens identify signed-in users.

You can also supply an existing app using the `ATLAS_GITHUB_*` and
`ATLAS_WEBHOOK_SECRET` environment variables. Its callback must be
`https://YOUR_HOST/auth/callback` and webhook endpoint
`https://YOUR_HOST/webhooks/github`. Mount its private key read-only and set
`ATLAS_GITHUB_PRIVATE_KEY_FILE` to the container path. The one-time wizard is
skipped when all credentials are present.

## 3. Use the UI

Caddy obtains and renews HTTPS certificates. Open your hostname, sign in with
GitHub, then choose **Add repository**. Select an installed repository or paste a
public GitHub URL. The first complete import runs in the background; its progress
is visible on Repositories. No commits reachable from the default branch are
sampled out. Only that branch's reachable history is imported; unmerged branches
are outside this view.

Each repo has **Refresh**, a scheduled check (15 minutes, hourly, six-hourly,
daily, or off), and automatic push-triggered refresh. One repository job runs at
a time and uses the configured Git worker budget. Other repos and the previous
completed version stay readable. Jobs resume after restarts; queued push events
are persisted and repeated delivery IDs are deduplicated.

**Share** copies the current repo, view and filters. By default, it also includes
`index=COMMIT_SHA`, freezing the completed analysis. Uncheck the option to share a
live link. Indexed Git objects are retained even after force-pushes. Archiving a
repo hides its analysis and shared links while preserving local data; adding it
again restores access. Removing a repository from the GitHub App installation
also archives it when GitHub delivers that event.

## Updates, storage and recovery

```sh
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

The application and HTTPS proxy use Go 1.26.9 and patched HTTP dependencies.
The proxy is built from the pinned module in `deploy/caddy`, because the upstream
Caddy 2.11.7 image still embeds an older Go runtime. Both services run with a
read-only root filesystem, no privilege escalation, and restricted capabilities.
Only Caddy publishes ports: TCP 80/443 and UDP 443 (HTTP/3). Port 8789, Caddy's
admin endpoint, and the Docker socket must not be published or mounted.

Check dependencies for newly disclosed vulnerabilities before deployment:

```sh
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
(cd deploy/caddy && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...)
```

Keep Docker Engine and the host's security updates current. Docker-published
ports can bypass UFW: verify the Docker forwarding rules and test from outside
the VM, including IPv6 when enabled. Preserve the firewall rules across Docker
and host restarts. Do not close unrelated existing services without checking
their intended use.

The `atlas_data` volume contains `workspace.sqlite`, `repos/`, `indexes/` and
`affiliations.yaml`, `github-app.json` and `github-app.pem`. It is private application data, including cached source and
session credentials. Keep it on local disk with restrictive permissions. The
worker is a separate process; `GOMEMLIMIT` is a soft budget **per process**, not a
combined hard container limit. Allow headroom for both the web process and worker.

Back up the complete data volume plus `.env` to private,
encrypted storage. For a simple consistent backup, stop atlas, archive the volume,
then start atlas again. Keep SQLite WAL sidecars with their databases; never copy
only a live `.sqlite` file. Test restore on a separate instance. Hetzner server
snapshots do not include attached Hetzner Volumes; back those up separately.

The repo indexes reuse completed commit states when the default branch advances.
Identity/affiliation rules are fixed when an index is opened. Don't edit
`affiliations.yaml` on a live instance; changing attribution rules can invalidate
old generation indexes. Per-repo editable rule versions are not implemented yet.

## Local hosted-mode preview

```sh
go build -o /tmp/ghca ./cmd/ghca
/tmp/ghca serve --dev --listen 127.0.0.1:8789 \
  --public-url http://127.0.0.1:8789 --data-dir /tmp/atlas-preview \
  --organization conduktor --email-domain conduktor.io
```

This explicitly disables authentication and can only bind to a loopback IP.
Public GitHub repositories work without app credentials. Production requires HTTPS, an organization and initial administrators. Without
GitHub App credentials, `--bootstrap` serves only a token-protected registration
page; no repository APIs or anonymous workspace are exposed. Without that flag,
missing credentials stop startup. Never use a tunnel or reverse proxy in front of development mode.
