import QtQuick 2.7
import Lomiri.Components 1.3
import QtQuick.Layouts 1.3

Page {
    id: page
    property var app
    property var profile        // null when adding

    property var groups: []
    property bool loadingGroups: false
    property string groupsError: ''

    header: PageHeader {
        title: page.profile ? i18n.tr('Edit VPN') : i18n.tr('Add VPN')
        trailingActionBar.actions: [
            Action {
                iconName: 'ok'
                text: i18n.tr('Save')
                enabled: hostField.text.trim() !== ''
                onTriggered: {
                    var p = {
                        id: page.profile ? page.profile.id : '',
                        name: nameField.text.trim(),
                        host: hostField.text.trim(),
                        username: userField.text.trim(),
                        authgroup: groupField.text.trim(),
                        password: passField.text,
                        save_password: saveSwitch.checked
                    };
                    app.saveProfile(p, function (res) {
                        if (res.ok) app.pop();
                        else errorLabel.text = res.msg || '';
                    });
                }
            }
        ]
    }

    Flickable {
        anchors { fill: parent; topMargin: page.header.height }
        contentHeight: col.height + units.gu(6)
        clip: true

        ColumnLayout {
            id: col
            anchors { left: parent.left; right: parent.right; top: parent.top
                      margins: units.gu(2) }
            spacing: units.gu(2)

            Label { text: i18n.tr('Name'); Layout.fillWidth: true }
            TextField {
                id: nameField
                Layout.fillWidth: true
                placeholderText: i18n.tr('University VPN')
                text: page.profile ? (page.profile.name || '') : ''
            }

            Label { text: i18n.tr('Gateway'); Layout.fillWidth: true }
            TextField {
                id: hostField
                Layout.fillWidth: true
                placeholderText: i18n.tr('vpn.example.edu')
                inputMethodHints: Qt.ImhUrlCharactersOnly | Qt.ImhNoAutoUppercase |
                                  Qt.ImhNoPredictiveText
                text: page.profile ? (page.profile.host || '') : ''
            }

            Label { text: i18n.tr('Username'); Layout.fillWidth: true }
            TextField {
                id: userField
                Layout.fillWidth: true
                inputMethodHints: Qt.ImhNoAutoUppercase | Qt.ImhNoPredictiveText
                text: page.profile ? (page.profile.username || '') : ''
            }

            Label { text: i18n.tr('Password'); Layout.fillWidth: true }
            TextField {
                id: passField
                Layout.fillWidth: true
                echoMode: TextInput.Password
                placeholderText: page.profile && page.profile.has_password
                                 ? i18n.tr('(unchanged)') : ''
            }

            RowLayout {
                Layout.fillWidth: true
                spacing: units.gu(2)
                Label {
                    Layout.fillWidth: true
                    wrapMode: Text.WordWrap
                    text: i18n.tr('Remember password')
                }
                Switch {
                    id: saveSwitch
                    checked: page.profile ? !!page.profile.has_password : false
                }
            }
            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Small
                color: theme.palette.normal.backgroundSecondaryText
                // Say this plainly rather than implying a security it does not
                // have: Ubuntu Touch has no keyring an app can use.
                text: i18n.tr('Stored in this app\'s private folder, which other apps cannot read. It is not encrypted.')
            }

            // ---- auth group -------------------------------------------------
            Label { text: i18n.tr('Login group'); Layout.fillWidth: true }
            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                textSize: Label.Small
                color: theme.palette.normal.backgroundSecondaryText
                text: i18n.tr('Many gateways require one. It can be fetched without signing in.')
            }
            TextField {
                id: groupField
                Layout.fillWidth: true
                inputMethodHints: Qt.ImhNoAutoUppercase | Qt.ImhNoPredictiveText
                text: page.profile ? (page.profile.authgroup || '') : ''
            }
            RowLayout {
                Layout.fillWidth: true
                spacing: units.gu(1)
                Button {
                    text: i18n.tr('Fetch groups')
                    enabled: hostField.text.trim() !== '' && !page.loadingGroups
                    onClicked: {
                        page.loadingGroups = true;
                        page.groupsError = '';
                        app.discoverGroups(hostField.text.trim(), function (res) {
                            page.loadingGroups = false;
                            page.groups = res.groups || [];
                            if (!res.ok) page.groupsError = res.msg || '';
                            else if (page.groups.length === 0)
                                page.groupsError = i18n.tr('This gateway offers no group choice.');
                        });
                    }
                }
                ActivityIndicator { running: page.loadingGroups; visible: running }
            }
            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                visible: page.groupsError !== ''
                textSize: Label.Small
                text: page.groupsError
            }
            Repeater {
                model: page.groups
                delegate: Button {
                    Layout.fillWidth: true
                    text: modelData
                    color: groupField.text === modelData ? theme.palette.normal.positive
                                                         : theme.palette.normal.foreground
                    onClicked: groupField.text = modelData
                }
            }

            Label {
                id: errorLabel
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                color: theme.palette.normal.negative
                visible: text !== ''
            }
        }
    }
}
