import QtQuick 2.7
import Lomiri.Components 1.3
import Qt.labs.settings 1.0
import io.thp.pyotherside 1.4

MainView {
    id: root
    // Must match the manifest `name` exactly, or the confined storage paths --
    // and the AppArmor rules that grant them -- do not line up.
    applicationName: 'ocvpn.franzthiemann'
    automaticOrientation: true
    width: units.gu(45)
    height: units.gu(75)

    // All app state lives here, not in the pages. Pages are pushed and popped,
    // so a page created mid-connect would otherwise miss every event that had
    // already fired.
    property bool ready: false
    property string binDir: ''
    property string setupError: ''
    property var setup: ({})            // what the user must type into Settings
    property var profiles: []
    property var bridge: ({ state: 'down' })
    property string phase: ''           // '', authenticating, starting, up, error
    property string lastError: ''
    property string activeProfileId: ''

    readonly property bool busy: phase === 'authenticating' || phase === 'starting'
    readonly property bool tunnelUp: bridge.state === 'up'

    signal fingerprintNeeded(string profileId, string fingerprint)

    // Whether the one-time setup has been acknowledged. UI-only state, so it
    // lives in QML settings rather than troubling the backend.
    property alias setupSeen: settings.setupSeen
    Settings {
        id: settings
        property bool setupSeen: false
    }

    // ---- thin wrappers: pages call these, never `py` directly ---------------

    function refreshProfiles(cb) {
        if (!ready) return;
        py.call('backend.list_profiles', [], function (res) {
            if (res.ok) root.profiles = res.profiles;
            if (cb) cb(res);
        });
    }

    function saveProfile(profile, cb) {
        py.call('backend.save_profile', [profile], function (res) {
            root.refreshProfiles();
            if (cb) cb(res);
        });
    }

    function deleteProfile(id, cb) {
        py.call('backend.delete_profile', [id], function (res) {
            root.refreshProfiles();
            if (cb) cb(res);
        });
    }

    function discoverGroups(host, cb) { py.call('backend.discover_groups', [host], cb); }
    function probeFingerprint(host, cb) { py.call('backend.probe_fingerprint', [host], cb); }

    function connectProfile(id, password, extra) {
        root.lastError = '';
        root.activeProfileId = id;
        root.phase = 'authenticating';
        py.call('backend.connect', [id, password || '', extra || []], function (res) {
            if (!res.ok) {
                root.phase = 'error';
                root.lastError = res.msg || '';
            }
        });
    }

    function disconnect() {
        py.call('backend.disconnect', [], function (res) {
            root.phase = '';
            root.refreshStatus();
            if (!res.ok) root.lastError = res.msg || '';
        });
    }

    function refreshStatus() {
        if (!ready) return;
        py.call('backend.status', [], function (res) {
            root.bridge = res;
            // The tunnel outlives the UI, so a running bridge found at startup
            // means we are connected even though this session never connected.
            if (res.state === 'up' && !root.busy) root.phase = 'up';
            else if (res.state !== 'up' && root.phase === 'up') root.phase = '';
        });
    }

    // Clipboard.push() is the one that actually works under Wayland
    // confinement; TextField.copy() silently does nothing.
    function copy(label, value) {
        if (!value) return;
        Clipboard.push(value);
        toast.show(i18n.tr('%1 copied').arg(label));
    }

    // The values the user has to reproduce in Settings, in the order the VPN
    // editor asks for them.
    function setupRows() {
        return [
            { label: i18n.tr('Remote'),   value: (setup.remote || '') + ':' + (setup.port || '') },
            { label: i18n.tr('Transport'),
              value: (setup.protocol || 'tcp') === 'tcp'
                     ? i18n.tr('TCP — tick “Use a TCP connection”')
                     : i18n.tr('UDP — untick “Use a TCP connection”') },
            { label: i18n.tr('Username'), value: setup.vpn_user || '' },
            { label: i18n.tr('Password'), value: setup.vpn_pass || '' },
            { label: i18n.tr('CA certificate'), value: setup.ca_cert || '' }
        ];
    }

    function profileById(id) {
        for (var i = 0; i < profiles.length; i++)
            if (profiles[i].id === id) return profiles[i];
        return null;
    }

    Python {
        id: py
        Component.onCompleted: {
            addImportPath(Qt.resolvedUrl('../src/'));
            importModule('backend', function () {
                py.call('backend.init', [], function (res) {
                    if (res.ok) {
                        root.binDir = res.bin_dir;
                        root.setup = res.setup || ({});
                        root.setupError = res.setup_error || '';
                    } else {
                        root.setupError = res.msg || '';
                    }
                    root.ready = true;
                    root.refreshProfiles();
                    root.refreshStatus();
                });
            });
        }
        onReceived: {
            // data == [event, payload] from pyotherside.send()
            if (data[0] === 'connect_state') {
                var p = data[1];
                root.phase = p.phase;
                if (p.phase === 'error') {
                    root.lastError = p.msg || '';
                    if (p.needs_fingerprint)
                        root.fingerprintNeeded(root.activeProfileId, p.fingerprint);
                }
                root.refreshStatus();
            }
        }
        onError: {
            console.log('python error: ' + traceback);
            root.lastError = 'Internal error; see the application log.';
        }
    }

    Timer {
        interval: 2000
        running: root.ready
        repeat: true
        onTriggered: root.refreshStatus()
    }

    PageStack {
        id: stack
        Component.onCompleted: stack.push(Qt.resolvedUrl('pages/ProfileListPage.qml'),
                                          { app: root })
    }

    // The two prerequisites cannot be done from inside the app, and neither is
    // discoverable. Show them once, before anything else, rather than letting
    // the first connection fail in a way that looks like a network fault.
    onReadyChanged: {
        if (ready && !root.setupSeen)
            root.push('SetupPage.qml', { firstRun: true });
    }

    Rectangle {
        id: toast
        function show(msg) { toastLabel.text = msg; toastAnim.restart(); }
        anchors { bottom: parent.bottom; horizontalCenter: parent.horizontalCenter
                  bottomMargin: units.gu(8) }
        width: toastLabel.width + units.gu(4)
        height: toastLabel.height + units.gu(2)
        radius: units.gu(0.5)
        color: theme.palette.normal.overlay
        opacity: 0
        z: 100
        Label { id: toastLabel; anchors.centerIn: parent }
        SequentialAnimation {
            id: toastAnim
            NumberAnimation { target: toast; property: 'opacity'; to: 1; duration: 150 }
            PauseAnimation { duration: 1400 }
            NumberAnimation { target: toast; property: 'opacity'; to: 0; duration: 400 }
        }
    }

    function push(page, props) {
        var p = props || ({});
        p.app = root;
        stack.push(Qt.resolvedUrl('pages/' + page), p);
    }
    function pop() { stack.pop(); }
}
