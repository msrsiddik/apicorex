// CORE_MASTER_KEY comes from the Jenkins Credentials Store (a Secret text
// credential with that exact ID), not from a parameter. It seals the secrets
// in Core's config store, so it must be the same on every deploy: a password
// parameter is not remembered, and Core started with a different key cannot
// read anything the dashboard saved. **The credential must exist before the
// first deploy**, or the Deploy stage fails with "Could not find credentials
// entry". Generate it once with `openssl rand -base64 32`, and keep a copy
// somewhere other than Jenkins — losing it loses every stored secret, the
// snapshots included.

// minutes reads a timeout parameter: a whole number of minutes, or the
// default when it is blank or not a number. A parameter added to this file is
// null on the first build after it lands — Jenkins learns a job's parameters
// by running it — so every timeout needs a default here, not only in the
// parameter's own defaultValue.
//
// Plain regex and Integer.parseInt rather than Groovy's isInteger/toInteger:
// the sandbox this file runs in has refused helpers like those before.
def minutes(value, int fallback) {
    def v = value == null ? '' : value.toString().trim()
    return (v ==~ /[1-9][0-9]{0,3}/) ? Integer.parseInt(v) : fallback
}

pipeline {
    agent any

    options {
        timestamps()
        disableConcurrentBuilds()
        // Room for a build with an empty compile cache — a fresh agent, or a
        // pruned one — which takes most of fifteen minutes on its own. Was 15,
        // which a cold build of the image first overran on 2026-10-09: the job
        // was killed mid-compile and the old container kept serving.
        //
        // The stages that can run long carry limits of their own
        // (TEST_TIMEOUT_MINUTES, DEPLOY_TIMEOUT_MINUTES), so a timeout names
        // the stage that ran over; this one is the backstop for the whole job.
        timeout(time: minutes(params.JOB_TIMEOUT_MINUTES, 60), unit: 'MINUTES')
    }

    // All blank by default — docker-compose.yml's ${VAR:-default} only falls
    // back on an empty value, so an untouched build keeps compose's own
    // defaults. Fill one in via "Build with Parameters" to override just that
    // value for this run.
    parameters {
        // ── Deploy ──────────────────────────────────────────────────────────
        // Picks which Jenkins agent the Deploy stage runs on, so one job can
        // target dev, staging, or prod just by switching this value. Dashboard,
        // Vet, Test and Build always run on whichever agent `agent any` picks
        // at the top of the pipeline — only Deploy re-pins to this one. Label
        // the corresponding agent under Manage Jenkins > Nodes.
        choice(name: 'TARGET_SERVER', choices: ['dev-server', 'staging-server', 'prod-server'], description: 'Jenkins agent/node to deploy to.')
        // Off by default since this job builds `develop`. A build is worth
        // running for Checks and Test on every commit; deploying is a decision,
        // and the branch being built is the one work lands on. Tick it, with
        // TARGET_SERVER, for the build that is meant to ship.
        booleanParam(name: 'DEPLOY', defaultValue: false, description: 'Build the image and (re)start the container on TARGET_SERVER. Off means verify only.')
        booleanParam(name: 'DEPLOY_OWN_POSTGRES', defaultValue: false, description: 'Also start the shared Postgres container (docker-compose.yml\'s "postgres" service). Off by default: Core itself never touches a database, so this only ever starts one on the plugins\' behalf, and by the time there is anything to lose the database is usually already running somewhere this job did not put it. Tick it for a first deploy, or wherever this job is the thing that owns Postgres. Leaving it off does not stop a container that is already up — the deploy omits the compose profile rather than tearing anything down.')

        // ── Build and test ──────────────────────────────────────────────────
        // Time limits, in minutes. Raise one for a build known to be slow — a
        // cold image cache, a busy agent — rather than editing this file.
        string(name: 'JOB_TIMEOUT_MINUTES', defaultValue: '60', description: 'The whole build\'s limit, in minutes: a backstop over the stages\' own.')
        string(name: 'TEST_TIMEOUT_MINUTES', defaultValue: '20', description: 'The Test stage\'s limit, in minutes. go test gets five minutes less, so it reports a hang itself.')
        string(name: 'DEPLOY_TIMEOUT_MINUTES', defaultValue: '25', description: 'The Deploy stage\'s limit, in minutes: the image build and the container restart.')

        // ── Gateway ─────────────────────────────────────────────────────────
        string(name: 'CORE_PORT', defaultValue: '', description: 'Host+container port for Core (compose default: 9999)')
        string(name: 'CORE_BIND', defaultValue: '', description: 'Interface the gateway port publishes on (compose default: 0.0.0.0). Do not set 127.0.0.1 while plugins reach Core through host.docker.internal — see docker-compose.yml.')
        string(name: 'CORS_ALLOWED_ORIGINS', defaultValue: '', description: 'Comma-separated browser origins (compose default: unrestricted)')
        string(name: 'LOGIN_URL', defaultValue: '', description: 'Where a browser with no session is sent, e.g. /login. A path on this origin, not a URL. Blank keeps the JSON 401 an API client expects, which is not what a person navigating to a page should see.')
        string(name: 'PRODUCT_HOSTS', defaultValue: '', description: 'Hostnames that stand for one plugin surface, e.g. panel.example.com=/school,gl.example.com=/accounting. Needs the reverse proxy to preserve Host. A malformed entry stops Core from starting.')
        string(name: 'ONDEMAND_TLS_PORT', defaultValue: '', description: 'Turns on the on-demand TLS gate the reverse proxy asks before issuing a certificate for a tenant-owned hostname (compose default: off). 9998 to match Caddyfile.example.')
        string(name: 'OTEL_EXPORTER_OTLP_ENDPOINT', defaultValue: '', description: 'OTLP endpoint (compose default: tracing off)')

        // ── Shared Postgres ─────────────────────────────────────────────────
        string(name: 'POSTGRES_PORT', defaultValue: '', description: 'Host port for shared Postgres (compose default: 15432)')
        string(name: 'POSTGRES_USER', defaultValue: '', description: 'compose default: apicorex')
        string(name: 'POSTGRES_DB', defaultValue: '', description: 'compose default: apicorex')

        // ── Protection defaults ─────────────────────────────────────────────
        string(name: 'CONFIG_FILE', defaultValue: '', description: 'Path to per-plugin limit overrides YAML. Copied into the store once per plugin; after that set limits in the dashboard (Protection).')
        string(name: 'RATE_PER_SEC', defaultValue: '', description: 'for every plugin the dashboard does not set it for. compose default: 1000')
        string(name: 'BULKHEAD_MAX', defaultValue: '', description: 'for every plugin the dashboard does not set it for. compose default: 100')
        string(name: 'CB_THRESHOLD', defaultValue: '', description: 'for every plugin the dashboard does not set it for. compose default: 5')
        string(name: 'CB_RESET_TIMEOUT', defaultValue: '', description: 'for every plugin the dashboard does not set it for. compose default: 30s')
        string(name: 'REQUEST_TIMEOUT', defaultValue: '', description: 'how long a plugin may take to start answering (send headers); streams and downloads are not cut. For every plugin the dashboard does not set it for. compose default: 120s')
        string(name: 'HEALTH_INTERVAL', defaultValue: '', description: 'compose default: 30s')

        // ── Config store ────────────────────────────────────────────────────
        string(name: 'STORE_SNAPSHOT_INTERVAL', defaultValue: '', description: 'How often the config store snapshots itself onto its volume, Go duration; 0 turns it off (compose default: 24h)')
        string(name: 'STORE_SNAPSHOT_KEEP', defaultValue: '', description: 'Snapshots kept (compose default: 7)')
        string(name: 'STORE_BACKUP_REMOTE', defaultValue: '', description: 'rclone remote:path the store\'s snapshots are copied to, e.g. gdrive-crypt:core-store. Blank keeps them on the server only. Set, it needs the STORE_RCLONE_CONF_B64 credential.')

        // ── Secrets ─────────────────────────────────────────────────────────
        // CORE_MASTER_KEY is not here: it comes from the Credentials Store —
        // see the note at the top of this file.
        password(name: 'POSTGRES_PASSWORD', defaultValue: '', description: 'compose default: apicorex')
        password(name: 'PLUGIN_API_KEY', defaultValue: '', description: 'Shared secret with plugins (compose default: change-me-plugin-key)')
        password(name: 'APICOREX_SECRET', defaultValue: '', description: 'compose default: apicorex-secret')
    }

    environment {
        GOFLAGS = '-mod=mod'
    }

    stages {
        stage('Dashboard') {
            // cmd/apicorex/dashboard.go does //go:embed all:admin/out, so Vet
            // and Build (which run natively here, not through the Dockerfile's
            // own Node stage) need the static export to exist before either
            // will compile.
            steps {
                sh '''
                    cd cmd/apicorex/admin
                    npm ci
                    npm run build
                '''
            }
        }

        stage('Vet') {
            steps {
                sh 'go vet ./...'
            }
        }

        stage('Test') {
            options {
                // go test's own -timeout is five minutes less than this, so
                // go test is the one that reports a hang, with a goroutine
                // dump, rather than Jenkins with nothing.
                timeout(time: minutes(params.TEST_TIMEOUT_MINUTES, 20), unit: 'MINUTES')
            }
            environment {
                TEST_MINUTES = "${minutes(params.TEST_TIMEOUT_MINUTES, 20)}"
            }
            steps {
                // -count=1 so a rerun actually runs: without it Go reports
                // "(cached)" per package and executes nothing, which is easy to
                // read as a green test run that never happened.
                sh '''
                    # Five minutes inside the Test stage's limit, never under five.
                    go_timeout=$(( TEST_MINUTES - 5 ))
                    [ "$go_timeout" -lt 5 ] && go_timeout=5
                    go test ./... -v -count=1 -timeout "${go_timeout}m"
                '''
            }
        }

        stage('Build') {
            steps {
                sh 'CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o out/apicorex ./cmd/apicorex'
            }
        }

        stage('Deploy') {
            // Builds the image and (re)starts the container via compose, which
            // also owns the shared Postgres + Redis every plugin depends on.
            //
            // Runs on whichever agent TARGET_SERVER names, not wherever the
            // earlier stages happened to land — Jenkins re-checks out the repo
            // on that node automatically because this stage declares its own
            // agent, so the compose build below always has the source it needs.
            //
            // Every param above passes through as an env var; docker-compose.yml's
            // ${VAR:-default} falls back to its own default when the param was
            // left blank, so an untouched build behaves exactly as before.
            // beforeAgent, or Jenkins allocates the node and clones the repo
            // onto it before deciding not to deploy — which, with TARGET_SERVER
            // set to prod-server, means occupying the production agent to do
            // nothing.
            when {
                beforeAgent true
                expression { params.DEPLOY }
            }
            agent { label params.TARGET_SERVER }
            options {
                // The image build and the container restart: quick from cached
                // layers, minutes when a dependency or the UI changed.
                timeout(time: minutes(params.DEPLOY_TIMEOUT_MINUTES, 25), unit: 'MINUTES')
            }
            environment {
                DEPLOY_OWN_POSTGRES = "${params.DEPLOY_OWN_POSTGRES}"
                CORE_PORT = "${params.CORE_PORT}"
                POSTGRES_PORT = "${params.POSTGRES_PORT}"
                POSTGRES_USER = "${params.POSTGRES_USER}"
                POSTGRES_DB = "${params.POSTGRES_DB}"
                CORS_ALLOWED_ORIGINS = "${params.CORS_ALLOWED_ORIGINS}"
                LOGIN_URL = "${params.LOGIN_URL}"
                PRODUCT_HOSTS = "${params.PRODUCT_HOSTS}"
                ONDEMAND_TLS_PORT = "${params.ONDEMAND_TLS_PORT}"
                CORE_BIND = "${params.CORE_BIND}"
                OTEL_EXPORTER_OTLP_ENDPOINT = "${params.OTEL_EXPORTER_OTLP_ENDPOINT}"
                CONFIG_FILE = "${params.CONFIG_FILE}"
                RATE_PER_SEC = "${params.RATE_PER_SEC}"
                BULKHEAD_MAX = "${params.BULKHEAD_MAX}"
                CB_THRESHOLD = "${params.CB_THRESHOLD}"
                CB_RESET_TIMEOUT = "${params.CB_RESET_TIMEOUT}"
                REQUEST_TIMEOUT = "${params.REQUEST_TIMEOUT}"
                HEALTH_INTERVAL = "${params.HEALTH_INTERVAL}"
                STORE_SNAPSHOT_INTERVAL = "${params.STORE_SNAPSHOT_INTERVAL}"
                STORE_SNAPSHOT_KEEP = "${params.STORE_SNAPSHOT_KEEP}"
                STORE_BACKUP_REMOTE = "${params.STORE_BACKUP_REMOTE}"
                // From the Credentials Store, not a parameter — see the note
                // at the top of this file.
                CORE_MASTER_KEY = credentials('CORE_MASTER_KEY')
                POSTGRES_PASSWORD = "${params.POSTGRES_PASSWORD}"
                PLUGIN_API_KEY = "${params.PLUGIN_API_KEY}"
                APICOREX_SECRET = "${params.APICOREX_SECRET}"
            }
            steps {
                // The rclone config only when off-site copies are on:
                // credentials() fails a build that cannot find one. It is the
                // config base64-encoded (base64 -w0 rclone.conf) in a Secret
                // text credential, passed as an environment variable rather
                // than a mounted file, which Jenkins deletes when this step
                // ends and a later container restart would then miss.
                script {
                    def creds = []
                    if (params.STORE_BACKUP_REMOTE?.trim()) {
                        creds << string(credentialsId: 'STORE_RCLONE_CONF_B64', variable: 'STORE_RCLONE_CONF_B64')
                    }
                    withCredentials(creds) {
                sh '''
                    set -eu
                    PROFILE_ARGS=""
                    if [ "$DEPLOY_OWN_POSTGRES" = "true" ]; then
                        PROFILE_ARGS="--profile with-postgres"
                    fi
                    docker compose $PROFILE_ARGS up -d --build

                    if [ "$DEPLOY_OWN_POSTGRES" = "true" ]; then
                        echo "Postgres connection URL for the plugin repos (password omitted — it is a Jenkins secret parameter):"
                        echo "postgres://${POSTGRES_USER:-apicorex}:<POSTGRES_PASSWORD>@host.docker.internal:${POSTGRES_PORT:-15432}/${POSTGRES_DB:-apicorex}?sslmode=disable"
                    fi
                '''
                    }
                }
            }
        }
    }

    post {
        always {
            cleanWs()
        }
    }
}
