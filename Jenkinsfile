pipeline {
    agent any

    options {
        timestamps()
        disableConcurrentBuilds()
        timeout(time: 15, unit: 'MINUTES')
    }

    // All blank by default — docker-compose.yml's ${VAR:-default} only falls
    // back on an empty value, so an untouched build keeps compose's own
    // defaults. Fill one in via "Build with Parameters" to override just that
    // value for this run.
    parameters {
        // Picks which Jenkins agent the Deploy stage runs on, so one job can
        // target dev, staging, or prod just by switching this value. Dashboard,
        // Vet, Test and Build always run on whichever agent `agent any` picks
        // at the top of the pipeline — only Deploy re-pins to this one. Label
        // the corresponding agent under Manage Jenkins > Nodes.
        choice(name: 'TARGET_SERVER', choices: ['dev-server', 'staging-server', 'prod-server'], description: 'Jenkins agent/node to deploy to.')
        booleanParam(name: 'DEPLOY_OWN_POSTGRES', defaultValue: true, description: 'Also start the shared Postgres container (docker-compose.yml\'s "postgres" service). Turn off when plugins should point at a Postgres that already runs elsewhere — Core itself never touches the database either way.')
        string(name: 'CORE_PORT', defaultValue: '', description: 'Host+container port for Core (compose default: 9999)')
        string(name: 'POSTGRES_PORT', defaultValue: '', description: 'Host port for shared Postgres (compose default: 15432)')
        string(name: 'POSTGRES_USER', defaultValue: '', description: 'compose default: apicorex')
        string(name: 'POSTGRES_DB', defaultValue: '', description: 'compose default: apicorex')
        string(name: 'CORS_ALLOWED_ORIGINS', defaultValue: '', description: 'Comma-separated browser origins (compose default: unrestricted)')
        string(name: 'OTEL_EXPORTER_OTLP_ENDPOINT', defaultValue: '', description: 'OTLP endpoint (compose default: tracing off)')
        string(name: 'CONFIG_FILE', defaultValue: '', description: 'Path to per-plugin limit overrides YAML')
        string(name: 'RATE_PER_SEC', defaultValue: '', description: 'compose default: 1000')
        string(name: 'BULKHEAD_MAX', defaultValue: '', description: 'compose default: 100')
        string(name: 'CB_THRESHOLD', defaultValue: '', description: 'compose default: 5')
        string(name: 'CB_RESET_TIMEOUT', defaultValue: '', description: 'compose default: 30s')
        string(name: 'REQUEST_TIMEOUT', defaultValue: '', description: 'compose default: 30s')
        string(name: 'HEALTH_INTERVAL', defaultValue: '', description: 'compose default: 30s')
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
            steps {
                sh 'go test ./... -v'
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
            agent { label params.TARGET_SERVER }
            environment {
                DEPLOY_OWN_POSTGRES = "${params.DEPLOY_OWN_POSTGRES}"
                CORE_PORT = "${params.CORE_PORT}"
                POSTGRES_PORT = "${params.POSTGRES_PORT}"
                POSTGRES_USER = "${params.POSTGRES_USER}"
                POSTGRES_DB = "${params.POSTGRES_DB}"
                CORS_ALLOWED_ORIGINS = "${params.CORS_ALLOWED_ORIGINS}"
                OTEL_EXPORTER_OTLP_ENDPOINT = "${params.OTEL_EXPORTER_OTLP_ENDPOINT}"
                CONFIG_FILE = "${params.CONFIG_FILE}"
                RATE_PER_SEC = "${params.RATE_PER_SEC}"
                BULKHEAD_MAX = "${params.BULKHEAD_MAX}"
                CB_THRESHOLD = "${params.CB_THRESHOLD}"
                CB_RESET_TIMEOUT = "${params.CB_RESET_TIMEOUT}"
                REQUEST_TIMEOUT = "${params.REQUEST_TIMEOUT}"
                HEALTH_INTERVAL = "${params.HEALTH_INTERVAL}"
                POSTGRES_PASSWORD = "${params.POSTGRES_PASSWORD}"
                PLUGIN_API_KEY = "${params.PLUGIN_API_KEY}"
                APICOREX_SECRET = "${params.APICOREX_SECRET}"
            }
            steps {
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

    post {
        always {
            cleanWs()
        }
    }
}
