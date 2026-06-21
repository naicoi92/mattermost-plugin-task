// Test mocks for the mattermost-redux channel/user selectors used by
// TaskSidebar. The real selectors live deep in the mattermost-redux package,
// whose @mattermost/* dependency chain relies on package.json "exports" —
// unsupported by Jest 27's resolver (same reason mattermost-redux/client is
// mocked). These stubs read the test state shape the host store would carry.
//
// In tests the Redux store is usually empty ({}); these selectors tolerate a
// missing entities slice by returning '' / undefined so TaskSidebar falls back
// to its channelID/currentUserID props.

function getCurrentChannelId(state) {
    return state && state.entities && state.entities.channels ?
        state.entities.channels.currentChannelId :
        '';
}

function getChannel(state, channelId) {
    if (!channelId) {
        return undefined;
    }
    const channels = state && state.entities && state.entities.channels ?
        state.entities.channels.channels :
        undefined;
    return channels ? channels[channelId] : undefined;
}

function getCurrentUserId(state) {
    return state && state.entities && state.entities.users ?
        state.entities.users.currentUserId :
        '';
}

module.exports = {
    getCurrentChannelId,
    getChannel,
    getCurrentUserId,
};
