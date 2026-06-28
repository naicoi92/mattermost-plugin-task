// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Jest mock for react-router-dom. react-router-dom is a webpack external
// (webpack.config.js) supplied by the Mattermost host at runtime — it is
// intentionally NOT installed as a package dependency, so Jest cannot resolve
// the real module. The TaskDeepLink route component imports useHistory /
// useParams from it. Mapped via Jest's moduleNameMapper in package.json so
// every test that transitively imports index.tsx resolves without a per-file
// jest.mock. Components needing richer router behavior override these in their
// own test via jest.mock('react-router-dom', () => ({...})).
module.exports = {
    useHistory: () => ({
        goBack: () => {},
        push: () => {},
        replace: () => {},
        length: 1,
    }),
    useParams: () => ({}),
};
