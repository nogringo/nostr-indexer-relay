// Signs a NIP-98 event with the NIP-07 extension and exchanges it for a session cookie.
async function signIn(button) {
  const status = document.querySelector("[data-sign-in-status]");
  if (!window.nostr) {
    status.textContent = "No Nostr extension found. Install one that supports NIP-07, then reload this page.";
    return;
  }

  button.disabled = true;
  status.textContent = "Waiting for your extension to sign.";
  try {
    const event = await window.nostr.signEvent({
      kind: 27235,
      created_at: Math.floor(Date.now() / 1000),
      tags: [["u", location.origin + "/admin/login"], ["method", "POST"]],
      content: "",
    });
    const response = await fetch("/admin/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ event, token: button.dataset.token || "" }),
    });
    if (response.ok) {
      // The setup page leads to the overview; any other page was asking to sign in, and reloads.
      if (button.dataset.token) location.assign("/admin");
      else location.reload();
      return;
    }
    status.textContent = (await response.text()).trim();
  } catch (error) {
    status.textContent = "The extension did not sign: " + (error.message || error);
  }
  button.disabled = false;
}

for (const button of document.querySelectorAll("[data-sign-in]")) {
  button.addEventListener("click", () => signIn(button));
}
