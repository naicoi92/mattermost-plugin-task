// Test mock for react-dom.
//
// react-dom is a webpack external (webpack.config.js `externals`, mapped to the
// global ReactDOM) supplied by the host at runtime, so it is not in node_modules
// and Jest cannot resolve it. The plugin imports only createPortal (the comment
// lightbox renders via document.body to escape the transformed RHS ancestor).
// In tests there is no DOM portal target that react-test-renderer understands,
// so createPortal returns its children directly — the lightbox only renders when
// lightboxUrl is set, which the unit tests never trigger.

function createPortal(children) {
    return children;
}

module.exports = {createPortal};
