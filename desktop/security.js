"use strict";

// Compare parsed origins: a textual prefix also accepts userinfo such as
// http://127.0.0.1:1234@external.example and ports such as 12345.
function isTrustedURL(candidate, baseURL) {
  if (!baseURL) return false;
  try {
    const expected = new URL(baseURL);
    const actual = new URL(candidate);
    return (
      expected.protocol === "http:" &&
      actual.origin === expected.origin &&
      !actual.username &&
      !actual.password
    );
  } catch (_) {
    return false;
  }
}

function assertTrustedSender(event, window, baseURL) {
  const contents = window?.webContents;
  if (
    !contents ||
    contents.isDestroyed() ||
    event?.sender !== contents ||
    !event.senderFrame ||
    event.senderFrame !== contents.mainFrame ||
    !isTrustedURL(event.senderFrame.url, baseURL)
  ) {
    throw new Error("拒绝来自不可信页面的桌面请求");
  }
}

module.exports = { isTrustedURL, assertTrustedSender };
