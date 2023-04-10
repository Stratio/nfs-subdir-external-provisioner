@Library('libpipelines') _
hose {
    EMAIL = 'eos@stratio.com'
    BUILDTOOL = 'make'
    DEVTIMEOUT = 60
    RELEASETIMEOUT = 60
    DEPLOYONPRS = true
    DEV = { config ->
            doHelmChart(conf:config, helmTarget:"helm-release")
    }
    GRYPE_TEST=true
}
