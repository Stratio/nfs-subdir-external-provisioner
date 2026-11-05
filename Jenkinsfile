@Library('libpipelines') _
hose {
    EMAIL = 'platform@stratio.com'
    BUILDTOOL = 'make'
    BUILDTOOL_IMAGE = 'golang:1.26.10'
    DEVTIMEOUT = 60
    RELEASETIMEOUT = 60
    DEPLOYONPRS = true
    GRYPE_TEST = true
    VERSIONING_TYPE = "semver"
    DEV = { config ->
            doPackage(conf:config, parameters: "HOME=/tmp")
            doDocker(conf:config)
            doHelmChart(conf:config, helmTarget:"helm-release")
    }
    BUILDTOOL_MEMORY_REQUEST = "1024Mi"
    BUILDTOOL_MEMORY_LIMIT = "10Gi"
}
