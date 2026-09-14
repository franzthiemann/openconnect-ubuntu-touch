import QtQuick 2.7
import Lomiri.Components 1.3
import QtQuick.Layouts 1.3

Page {
    id: page
    property var app
    // Shown automatically on first launch, where there is nothing to go back
    // to and the only sensible action is to read it and acknowledge.
    property bool firstRun: false

    header: PageHeader {
        // The back action is left in place even on first run: if the page is
        // dismissed without acknowledging, it simply appears again next launch,
        // which is the right behaviour for a prerequisite.
        title: page.firstRun ? i18n.tr('Before you start') : i18n.tr('One-time setup')
    }

    // Every value here has to be copied by hand, because a confined app is
    // denied NetworkManager's D-Bus API and cannot create the connection
    // itself.

    Flickable {
        anchors { fill: parent; topMargin: page.header.height }
        contentHeight: col.height + units.gu(6)
        clip: true

        ColumnLayout {
            id: col
            anchors { left: parent.left; right: parent.right; top: parent.top
                      margins: units.gu(2) }
            spacing: units.gu(2)

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                text: i18n.tr('This app brings the VPN tunnel up, but Ubuntu Touch itself has to switch it on. Two one-time steps are needed.')
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Large
                text: i18n.tr('1. Stop the system suspending this app')
            }
            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                // Without this nothing works at all, and the failure looks like
                // a network problem rather than a suspended process: the VPN is
                // switched on from the Settings app, so this app is in the
                // background at exactly the moment it has to answer, and a
                // suspended process cannot accept the connection.
                text: i18n.tr('Open UT Tweak Tool, and under Lifecycle exceptions add “OpenConnect VPN”. Without it Ubuntu Touch freezes this app the moment you switch to Settings, and the VPN connection will simply time out.')
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Large
                text: i18n.tr('2. Create the VPN connection')
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                visible: app.setupError !== ''
                color: theme.palette.normal.negative
                text: i18n.tr('Setup could not be prepared: %1').arg(app.setupError)
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Large
                text: i18n.tr('In Settings → VPN → Add')
                visible: false
            }

            Repeater {
                model: [{ label: i18n.tr('Type'), value: i18n.tr('OpenVPN') },
                        { label: i18n.tr('Auth type'), value: i18n.tr('Password') }]
                        .concat(app.setupRows())
                delegate: ListItem {
                    Layout.fillWidth: true
                    height: rowLayout.height + units.gu(2)
                    onClicked: app.copy(modelData.label, modelData.value)
                    ListItemLayout {
                        id: rowLayout
                        title.text: modelData.label
                        subtitle.text: modelData.value
                        subtitle.wrapMode: Text.WrapAnywhere
                        subtitle.maximumLineCount: 4
                        Icon {
                            SlotsLayout.position: SlotsLayout.Trailing
                            width: units.gu(2.5)
                            name: 'edit-copy'
                        }
                    }
                }
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Small
                color: theme.palette.normal.backgroundSecondaryText
                text: i18n.tr('Tap a row to copy it. The CA certificate is picked with the file browser in the VPN editor; the path above is where it lives.\n\nUnder Advanced, "Use a TCP connection" must agree with the Transport row above, or the connection simply times out.')
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Large
                text: i18n.tr('3. Tick “Only use connection for VPN resources”')
            }
            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                // Not a preference: without it NetworkManager makes the VPN the
                // default route, and the gateway itself then sits behind the
                // tunnel that is carrying it. Nothing reaches the network.
                text: i18n.tr('In the VPN connection\'s Advanced settings. This is required, not a preference — without it everything is routed into the tunnel, including this app\'s own connection to the gateway, and you lose all network access. The networks your VPN actually serves are routed through it either way.')
            }

            Button {
                Layout.fillWidth: true
                visible: page.firstRun
                color: theme.palette.normal.positive
                text: i18n.tr('Got it')
                onClicked: { app.setupSeen = true; app.pop(); }
            }
        }
    }

}
