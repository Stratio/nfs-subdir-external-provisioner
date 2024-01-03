@Library('libpipelines') _
hose {
    EMAIL = 'eos@stratio.com'
    BUILDTOOL = 'make'
    BUILDTOOL_IMAGE = 'golang:1.19'
    DEVTIMEOUT = 60
    RELEASETIMEOUT = 60
    VERSIONING_TYPE = 'stratioVersion-3-3'
    UPSTREAM_VERSION = '4.0.19'
    DEPLOYONPRS = true
    DEV = { config ->
            doPackage(conf:config, parameters: "HOME=/tmp")
            doDocker(conf:config)
            doHelmChart(conf:config, helmTarget:"helm-release")
    }
}
