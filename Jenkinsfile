pipeline {
    agent any

    options {
        timestamps()
        disableConcurrentBuilds()
        timeout(time: 15, unit: 'MINUTES')
    }

    environment {
        GOFLAGS   = '-mod=mod'
        IMAGE_TAG = "apicorex:${env.BUILD_NUMBER}"
    }

    stages {
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

        stage('Docker Image') {
            steps {
                sh "docker build -t ${IMAGE_TAG} -t apicorex:latest ."
            }
        }
    }

    post {
        always {
            cleanWs()
        }
    }
}
