#!/usr/bin/env sh
# Required TLC gate, including intentional counterexamples. Never count a parser
# failure/deadlock/tool absence as a reproduced invariant or temporal violation.
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ARTIFACT_DIR=${FORMAL_TLC_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/xds-tlc.XXXXXX")}
mkdir -p "$ARTIFACT_DIR"
ARTIFACT_DIR=$(CDPATH= cd -- "$ARTIFACT_DIR" && pwd)
JAR=${TLA2TOOLS_JAR:-$ARTIFACT_DIR/tla2tools.jar}
if ! test -f "$JAR"; then
    curl -fsSL https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar -o "$JAR"
fi
actual=$(shasum -a 256 "$JAR" | cut -d ' ' -f 1)
if test "$actual" != 936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88; then
    echo "TLC artifact checksum mismatch: $actual" >&2
    exit 1
fi
JAR=$(CDPATH= cd -- "$(dirname -- "$JAR")" && pwd)/$(basename -- "$JAR")
for cfg in "$SCRIPT_DIR"/*.cfg; do
    name=$(basename -- "$cfg" .cfg)
    if test "$name" = XdsAdsSotw && test "${TLC_INCLUDE_WIDE:-0}" != 1; then
        echo "DEFERRED XdsAdsSotw original bounds; required run uses XdsAdsSotwCI (RF-015)" | tee "$ARTIFACT_DIR/deferred-wide-model.txt"
        continue
    fi
    case "$name" in
        KrtRecoveryDroppedFanout) model=KrtRecovery; expected=temporal ;;
        KrtRecoveryWatchdogStaleReplay) model=KrtRecovery; expected=temporal ;;
        KrtRecoveryCoherentInput) model=KrtRecovery; expected=pass ;;
        KrtRecoveryWatchdog) model=KrtRecovery; expected=pass ;;
        ColdMissingCdsStarvationCurrent) model=ColdMissingCdsStarvation; expected=temporal ;;
        ColdMissingCdsStarvationTransient) model=ColdMissingCdsStarvation; expected=pass ;;
        ColdMissingCdsStarvationClassified) model=ColdMissingCdsStarvation; expected=pass ;;
        ReconnectWhileWarmingCurrent) model=ReconnectWhileWarming; expected=temporal ;;
        ReconnectWhileWarmingEndpointChange) model=ReconnectWhileWarming; expected=pass ;;
        ReconnectWhileWarmingUnconditionalFirstResponse) model=ReconnectWhileWarming; expected=pass ;;
        ReconnectWhileWarmingEdsCacheFallback) model=ReconnectWhileWarming; expected=pass ;;
        WarmTypeStarvationCurrent) model=WarmTypeStarvation; expected=temporal ;;
        WarmTypeStarvationIsolated) model=WarmTypeStarvation; expected=pass ;;
        ReachabilityIsNotLiveness) model=ReachabilityIsNotLiveness; expected=temporal ;;
        XdsEnvoyWarming*) model=XdsEnvoyWarming; expected=pass ;;
        XdsPerClientConvergence*) model=XdsPerClientConvergence; expected=pass ;;
        XdsPerClientPublication*) model=XdsPerClientPublication; expected=pass ;;
        XdsNamedEdsWatch*) model=XdsNamedEdsWatch; expected=pass ;;
        XdsReconnectRace13868*) model=XdsReconnectRace13868; expected=pass ;;
        XdsEdsSubset*) model=XdsEdsSubset; expected=pass ;;
        XdsAdsSotw*) model=XdsAdsSotw; expected=pass ;;
        *) echo "Unclassified TLC configuration $name" >&2; exit 1 ;;
    esac
    case "$name" in
        *NoPublishBug) expected=temporal ;;
        *Bug) expected=invariant ;;
    esac
    invariant=
    case "$name" in
        XdsEdsSubsetBug) invariant=NoOrphanEndpointResources ;;
        XdsEnvoyWarmingAckImpliesActiveBug) invariant=ActiveClustersHaveCDSAndEDS ;;
        XdsEnvoyWarmingEmptyCLAFlipBug) invariant=WarmRouteFlipHasReadyCLA ;;
        XdsEnvoyWarmingListenerBeforeRouteBug) invariant=ActiveListenerHasRouteConfig ;;
        XdsEnvoyWarmingRouteBeforeClusterBug) invariant=ActiveRouteReferencesActiveCluster ;;
        XdsNamedEdsWatchStaleExtraBug) invariant=ChangedSnapshotRequestRespondable ;;
        XdsNamedEdsWatchVersionReuseBug) invariant=ResourceSetChangeRequiresVersionChange ;;
        XdsPerClientConvergenceActivateBeforeEdsBug) invariant=ActiveSnapshotClosed ;;
        XdsPerClientConvergenceClearOnDeleteBug) invariant=DeleteRetainsLastGood ;;
        XdsPerClientConvergencePartialOverwriteBug) invariant=PartialDoesNotOverwriteCache ;;
        XdsPerClientConvergenceStaleEdsBug) invariant=CacheSnapshotClosed ;;
        XdsPerClientConvergenceVersionReuseBug) invariant=EDSResourceSetChangeChangesVersion ;;
        XdsPerClientPublication*Bug) invariant=CacheSnapshotCoherent ;;
        XdsReconnectRace13868Bug) invariant=ServedSnapshotReferencesResolved ;;
    esac
    log="$ARTIFACT_DIR/$name.log"

    result=0
    docker run --rm -v "$SCRIPT_DIR:/model:ro" -v "$JAR:/tla2tools.jar:ro" -w /model \
        eclipse-temurin:21-jre java -XX:+UseParallelGC -jar /tla2tools.jar \
        -metadir /tmp/states -config "$name.cfg" "$model.tla" > "$log" 2>&1 || result=$?
    case "$expected" in
        pass) test "$result" -eq 0 && grep -q 'Model checking completed. No error has been found.' "$log" ;;
        invariant) test "$result" -eq 12 && test -n "$invariant" && grep -Fq "Error: Invariant $invariant is violated." "$log" ;;
        temporal) test "$result" -eq 13 && grep -q '^Error: Temporal properties were violated\.' "$log" ;;
    esac || { cat "$log"; echo "Unexpected TLC outcome: $name expected $expected (exit $result)" >&2; exit 1; }
    echo "PASS $name expected=$expected exit=$result"
done
echo "TLC receipts: $ARTIFACT_DIR"
