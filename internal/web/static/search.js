// NIP-05 addresses are resolved here, in the visitor's browser: the server never fetches a URL
// taken from what someone typed.
function nip05Parts(query) {
  if (/^(nostr:)?(npub1|nprofile1)/.test(query) || /^[0-9a-f]{64}$/i.test(query)) return null;
  const match = /^(?:([^@\s]+)@)?([a-z0-9-]+(?:\.[a-z0-9-]+)+)$/i.exec(query);
  return match ? { name: (match[1] || "_").toLowerCase(), domain: match[2].toLowerCase() } : null;
}

async function lookUp(query, parts, status) {
  status.classList.remove("error");
  status.textContent = "Looking up " + query + ".";
  try {
    // NIP-05: redirects must be ignored.
    const response = await fetch(`https://${parts.domain}/.well-known/nostr.json?name=${encodeURIComponent(parts.name)}`, { redirect: "error" });
    if (!response.ok) throw new Error(`${parts.domain} answered ${response.status}`);
    const pubkey = ((await response.json()).names || {})[parts.name];
    if (!/^[0-9a-f]{64}$/.test(pubkey || "")) throw new Error(`${parts.domain} has no pubkey for ${parts.name}`);
    location.assign("/p/" + pubkey);
  } catch (error) {
    const reason = error instanceof TypeError ? `${parts.domain} did not answer, or does not allow lookups from other sites` : error.message;
    status.classList.add("error");
    status.textContent = `Could not look up ${query}: ${reason}. Search by npub or hex pubkey instead.`;
  }
}

for (const form of document.querySelectorAll("[data-search]")) {
  form.addEventListener("submit", (event) => {
    const query = form.querySelector("input[name=q]").value.trim();
    const parts = nip05Parts(query);
    if (!parts) return;
    event.preventDefault();
    lookUp(query, parts, document.querySelector("[data-search-status]"));
  });
}

for (const button of document.querySelectorAll("[data-copy]")) {
  const source = document.querySelector("[data-copy-source]");
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(source.value);
      button.textContent = "Copied";
      setTimeout(() => {
        button.textContent = "Copy";
      }, 2000);
    } catch {
      // Without clipboard access (a page served over plain http), select it for a manual copy.
      source.select();
    }
  });
}
