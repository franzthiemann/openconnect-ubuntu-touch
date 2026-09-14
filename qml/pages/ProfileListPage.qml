import QtQuick 2.7
import Lomiri.Components 1.3
import Lomiri.Components.Popups 1.3
import QtQuick.Layouts 1.3

Page {
    id: page
    property var app

    header: PageHeader {
        title: i18n.tr('OpenConnect VPN')
        trailingActionBar.actions: [
            Action {
                iconName: 'add'
                text: i18n.tr('Add')
                onTriggered: app.push('ProfileEditPage.qml', { profile: null })
            },
            Action {
                iconName: 'info'
                text: i18n.tr('Setup')
                onTriggered: app.push('SetupPage.qml', {})
            }
        ]
    }

    Connections {
        target: app
        // The import race: this page can exist before the backend is ready.
        onReadyChanged: if (app.ready) app.refreshProfiles()
        onFingerprintNeeded: PopupUtils.open(trustDialog, page,
                                             { profileId: profileId, fingerprint: fingerprint })
    }

    // ---- connection status banner ------------------------------------------
    Rectangle {
        id: banner
        anchors { top: page.header.bottom; left: parent.left; right: parent.right }
        height: content.height + units.gu(3)
        color: theme.palette.normal.foreground

        ColumnLayout {
            id: content
            anchors { left: parent.left; right: parent.right; verticalCenter: parent.verticalCenter
                      margins: units.gu(2) }
            spacing: units.gu(1)

            RowLayout {
                Layout.fillWidth: true
                spacing: units.gu(1.5)
                ActivityIndicator { running: app.busy; visible: running }
                Label {
                    Layout.fillWidth: true
                    wrapMode: Text.WordWrap
                    textSize: Label.Large
                    text: {
                        if (!app.ready) return i18n.tr('Starting…');
                        switch (app.phase) {
                        case 'authenticating': return i18n.tr('Signing in…');
                        case 'starting':       return i18n.tr('Starting the tunnel…');
                        case 'up':             return i18n.tr('Tunnel ready');
                        case 'error':          return i18n.tr('Could not connect');
                        default:               return i18n.tr('Not connected');
                        }
                    }
                }
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                visible: text !== ''
                color: app.phase === 'error' ? theme.palette.normal.negative
                                             : theme.palette.normal.backgroundSecondaryText
                text: {
                    if (app.phase === 'error') return app.lastError;
                    if (!app.tunnelUp) return '';
                    // The app cannot switch the VPN on: a confined app is denied
                    // NetworkManager's D-Bus API, so this is the user's step.
                    return app.bridge.client_up
                        ? i18n.tr('Address %1 · carrying traffic').arg(app.bridge.address || '')
                        : i18n.tr('Now switch the VPN on in Settings → VPN.');
                }
            }

            // The tunnel being up is only half the job: the user still has to
            // switch the connection on in Settings, because a confined app may
            // not drive NetworkManager. So put the values they need right here
            // at the moment they need them, rather than behind a header icon.
            ColumnLayout {
                Layout.fillWidth: true
                spacing: units.gu(0.5)
                visible: app.tunnelUp && !app.bridge.client_up

                Repeater {
                    model: app.setupRows()
                    delegate: AbstractButton {
                        Layout.fillWidth: true
                        height: units.gu(4)
                        onClicked: app.copy(modelData.label, modelData.value)
                        RowLayout {
                            anchors.fill: parent
                            spacing: units.gu(1)
                            Label {
                                text: modelData.label
                                textSize: Label.Small
                                color: theme.palette.normal.backgroundSecondaryText
                                Layout.preferredWidth: units.gu(13)
                            }
                            Label {
                                Layout.fillWidth: true
                                text: modelData.value
                                textSize: Label.Small
                                elide: Text.ElideMiddle
                            }
                            Icon { width: units.gu(2); height: width; name: 'edit-copy' }
                        }
                    }
                }

                Label {
                    Layout.fillWidth: true
                    wrapMode: Text.WordWrap
                    textSize: Label.Small
                    color: theme.palette.normal.backgroundSecondaryText
                    text: i18n.tr('Tap a value to copy it. This app must be exempt from suspension in UT Tweak Tool, or it cannot answer while you are in Settings.')
                }

                Button {
                    Layout.fillWidth: true
                    text: i18n.tr('Full setup instructions')
                    onClicked: app.push('SetupPage.qml', {})
                }
            }

            Button {
                Layout.fillWidth: true
                visible: app.tunnelUp || app.busy
                text: i18n.tr('Disconnect')
                onClicked: app.disconnect()
            }
        }
    }

    ListView {
        id: list
        anchors { top: banner.bottom; left: parent.left; right: parent.right; bottom: parent.bottom }
        clip: true
        model: app.profiles

        delegate: ListItem {
            height: layout.height + divider.height
            enabled: !app.busy && !app.tunnelUp

            leadingActions: ListItemActions {
                actions: Action {
                    iconName: 'delete'
                    onTriggered: app.deleteProfile(modelData.id)
                }
            }
            trailingActions: ListItemActions {
                actions: Action {
                    iconName: 'edit'
                    onTriggered: app.push('ProfileEditPage.qml', { profile: modelData })
                }
            }

            ListItemLayout {
                id: layout
                title.text: modelData.name || modelData.host
                subtitle.text: {
                    var bits = [modelData.host];
                    if (modelData.username) bits.push(modelData.username);
                    if (modelData.authgroup) bits.push(modelData.authgroup);
                    return bits.join(' · ');
                }
                Icon {
                    SlotsLayout.position: SlotsLayout.Trailing
                    width: units.gu(2)
                    name: 'next'
                }
            }

            onClicked: {
                if (modelData.has_password) {
                    app.connectProfile(modelData.id, '', []);
                } else {
                    PopupUtils.open(passwordDialog, page, { profileId: modelData.id,
                                                            profileName: layout.title.text });
                }
            }
        }
    }

    Label {
        anchors.centerIn: list
        width: parent.width - units.gu(8)
        horizontalAlignment: Text.AlignHCenter
        wrapMode: Text.WordWrap
        visible: app.ready && app.profiles.length === 0
        text: i18n.tr('No VPNs yet.\nUse + to add one.')
    }

    // ---- dialogs -----------------------------------------------------------

    Component {
        id: passwordDialog
        Dialog {
            id: pwDlg
            property string profileId
            property string profileName
            title: i18n.tr('Sign in')
            text: profileName

            TextField {
                id: pwField
                echoMode: TextInput.Password
                placeholderText: i18n.tr('Password')
                onAccepted: acceptButton.clicked()
            }
            Button {
                id: acceptButton
                text: i18n.tr('Connect')
                color: theme.palette.normal.positive
                enabled: pwField.text !== ''
                onClicked: {
                    app.connectProfile(pwDlg.profileId, pwField.text, []);
                    PopupUtils.close(pwDlg);
                }
            }
            Button {
                text: i18n.tr('Cancel')
                onClicked: PopupUtils.close(pwDlg)
            }
            Component.onCompleted: pwField.forceActiveFocus()
        }
    }

    Component {
        id: trustDialog
        Dialog {
            id: trustDlg
            property string profileId
            property string fingerprint
            title: i18n.tr('Trust this gateway?')
            text: i18n.tr('The gateway presented a certificate that is not signed by a known authority. Check this fingerprint against what your administrator published:\n\n%1').arg(fingerprint)

            Button {
                text: i18n.tr('Trust and connect')
                color: theme.palette.normal.positive
                onClicked: {
                    var p = app.profileById(trustDlg.profileId);
                    if (p) {
                        var updated = JSON.parse(JSON.stringify(p));
                        updated.servercert = trustDlg.fingerprint;
                        app.saveProfile(updated, function () {
                            PopupUtils.close(trustDlg);
                        });
                    } else {
                        PopupUtils.close(trustDlg);
                    }
                }
            }
            Button {
                text: i18n.tr('Cancel')
                onClicked: PopupUtils.close(trustDlg)
            }
        }
    }
}
