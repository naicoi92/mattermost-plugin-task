// Test mock for mattermost-redux/client.
//
// The real Client4 is imported by src/client.ts (delegated to in doFetch and
// getUserByUsername) so the plugin's requests carry the headers the Mattermost
// server requires (X-Requested-With, X-CSRF-Token on write methods,
// credentials: 'include'). In unit tests we can't load the real module: its
// deep @mattermost/* dependency chain relies on the package.json "exports"
// field, which Jest 27's resolver doesn't support.
//
// This mock reproduces Client4.getOptions's contract just closely enough for
// client.test.ts to assert that doFetch routes request options through it and
// that the resulting headers carry the CSRF token. It reads the MMCSRF cookie
// from document.cookie the same way the real implementation does, so setting
// document.cookie = 'MMCSRF=token' in a test makes the X-CSRF-Token header
// appear on non-GET requests.

function readCSRFToken() {
    if (typeof document === 'undefined' || typeof document.cookie === 'undefined') {
        return '';
    }
    const cookies = document.cookie.split(';');
    for (let i = 0; i < cookies.length; i++) {
        const cookie = cookies[i].trim();
        if (cookie.startsWith('MMCSRF=')) {
            return cookie.replace('MMCSRF=', '');
        }
    }
    return '';
}

const Client4 = {
    getOptions(options) {
        const newOptions = options || {};
        const headers = {
            'X-Requested-With': 'XMLHttpRequest',
        };
        if (newOptions.headers) {
            Object.assign(headers, newOptions.headers);
        }
        const method = (newOptions.method || 'GET').toLowerCase();
        if (method !== 'get') {
            const csrf = readCSRFToken();
            if (csrf) {
                headers['X-CSRF-Token'] = csrf;
            }
        }
        return Object.assign({}, newOptions, {headers, credentials: 'include'});
    },
};

module.exports = {Client4};
