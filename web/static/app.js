// ELD billing page: log in, then manage the company's cards.
//
// Rules this file follows:
//   - Card numbers are typed into Stripe's own iframe (Payment Element) and
//     sent by Stripe.js straight to Stripe. Our API only ever sees ids.
//   - Data from the API is put on the page with textContent, never
//     innerHTML: a company named "<img onerror=...>" stays plain text.
//   - The access token lives in sessionStorage (this tab only, gone when the
//     tab closes). Any script on the page could read it, which is why the
//     CSP allows no inline scripts and no scripts from other sites except
//     js.stripe.com.
"use strict";

const API = "/api/v1";
const TOKEN_KEY = "eld.access_token";

const state = {
  me: null,          // /auth/me
  companyId: null,   // whose billing is shown
  stripe: null,      // Stripe.js instance (created once we know the key)
  elements: null,    // Stripe Elements for the card being added
};

const $ = (id) => document.getElementById(id);

// ---------- API ----------

class ApiError extends Error {
  constructor(status, body) {
    super((body && body.error && body.error.message) || `HTTP ${status}`);
    this.status = status;
    this.code = body && body.error && body.error.code;
  }
}

async function api(method, path, body) {
  const headers = {};
  const token = sessionStorage.getItem(TOKEN_KEY);
  if (token) headers["Authorization"] = "Bearer " + token;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(API + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && path !== "/auth/login") {
    // Token expired (15 min) or revoked: back to the login form.
    logout();
    throw new ApiError(401, null);
  }
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

// ---------- login ----------

$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.target;
  const button = form.querySelector("button");
  $("login-error").textContent = "";
  button.disabled = true;
  try {
    const tokens = await api("POST", "/auth/login", {
      login: form.login.value.trim(),
      password: form.password.value,
    });
    sessionStorage.setItem(TOKEN_KEY, tokens.access_token);
    form.reset();
    await start();
  } catch (err) {
    $("login-error").textContent = err.status === 429
      ? "Too many attempts. Wait a moment and try again."
      : "Wrong login or password.";
  } finally {
    button.disabled = false;
  }
});

$("logout").addEventListener("click", logout);

function logout() {
  sessionStorage.removeItem(TOKEN_KEY);
  state.me = null;
  state.companyId = null;
  closeCardForm();
  $("session").hidden = true;
  $("billing-view").hidden = true;
  $("login-view").hidden = false;
}

// ---------- start ----------

async function start() {
  state.me = await api("GET", "/auth/me");
  const name = state.me.email || state.me.username;
  $("who").textContent = `${state.me.first_name} ${state.me.last_name} (${name})`;
  $("session").hidden = false;
  $("login-view").hidden = true;
  $("billing-view").hidden = false;

  if (state.me.company_id) {
    // Company users manage their own company only.
    state.companyId = state.me.company_id;
    $("company-picker").hidden = true;
  } else {
    await loadCompanies();
  }
  await loadCards();
}

async function loadCompanies() {
  const page = await api("GET", "/companies?page_size=100");
  const select = $("company-select");
  select.replaceChildren();
  for (const c of page.items) {
    const opt = document.createElement("option");
    opt.value = c.id;
    opt.textContent = `${c.name} (USDOT ${c.dot_number})`;
    select.append(opt);
  }
  $("company-picker").hidden = false;
  state.companyId = page.items.length ? page.items[0].id : null;
}

$("company-select").addEventListener("change", (ev) => {
  state.companyId = ev.target.value;
  closeCardForm();
  loadCards();
});

// ---------- cards ----------

function billingPath(rest) {
  return `/companies/${encodeURIComponent(state.companyId)}/billing${rest}`;
}

async function loadCards() {
  const list = $("card-list");
  const msg = $("cards-message");
  list.replaceChildren();
  msg.textContent = "";
  if (!state.companyId) {
    msg.textContent = "No company to manage yet.";
    return;
  }
  try {
    const res = await api("GET", billingPath("/payment-methods"));
    if (res.items.length === 0) {
      msg.textContent = "No card saved yet.";
    }
    for (const pm of res.items) list.append(cardRow(pm));
    return res.items;
  } catch (err) {
    // Without Stripe keys the billing routes do not exist at all.
    msg.textContent = err.status === 404 && err.message === "route not found"
      ? "Billing is not enabled on this server."
      : `Could not load cards: ${err.message}`;
  }
}

function cardRow(pm) {
  const li = document.createElement("li");

  const brand = document.createElement("span");
  brand.className = "brand";
  brand.textContent = pm.brand;

  const number = document.createElement("span");
  number.textContent = `•••• ${pm.last4}`;

  const exp = document.createElement("span");
  exp.className = "exp";
  exp.textContent = `expires ${String(pm.exp_month).padStart(2, "0")}/${pm.exp_year}`;

  const spacer = document.createElement("span");
  spacer.className = "spacer";

  li.append(brand, number, exp);
  if (pm.is_default) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = "Default";
    li.append(badge);
  }
  li.append(spacer);

  if (!pm.is_default) {
    li.append(smallButton("Make default", async () => {
      await api("PUT", billingPath(`/payment-methods/${encodeURIComponent(pm.id)}/default`));
      await loadCards();
    }));
  }
  li.append(smallButton("Remove", async () => {
    if (!confirm(`Remove ${pm.brand} •••• ${pm.last4}?`)) return;
    await api("DELETE", billingPath(`/payment-methods/${encodeURIComponent(pm.id)}`));
    await loadCards();
  }, true));
  return li;
}

function smallButton(label, onClick, secondary) {
  const b = document.createElement("button");
  b.className = secondary ? "small secondary" : "small";
  b.textContent = label;
  b.addEventListener("click", async () => {
    b.disabled = true;
    try {
      await onClick();
    } catch (err) {
      $("cards-message").textContent = err.message;
    } finally {
      b.disabled = false;
    }
  });
  return b;
}

// ---------- add a card ----------

$("add-card").addEventListener("click", async () => {
  const button = $("add-card");
  button.disabled = true;
  $("card-error").textContent = "";
  try {
    // 1. Our API creates a SetupIntent: a one-time permission for this
    //    browser to save one card for this company.
    const si = await api("POST", billingPath("/setup-intent"));

    // 2. Stripe.js shows Stripe's card form, bound to that SetupIntent.
    if (!state.stripe) state.stripe = Stripe(si.publishable_key);
    const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    state.elements = state.stripe.elements({
      clientSecret: si.client_secret,
      appearance: { theme: dark ? "night" : "stripe" },
    });
    state.elements.create("payment").mount("#payment-element");
    $("card-form").hidden = false;
  } catch (err) {
    $("cards-message").textContent = `Could not start: ${err.message}`;
  } finally {
    button.disabled = false;
  }
});

$("card-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const button = $("save-card");
  button.disabled = true;
  $("card-error").textContent = "";
  try {
    // 3. Stripe.js sends the card to Stripe (and shows 3-D Secure if the
    //    bank asks). Nothing about the card passes through our server.
    const { error, setupIntent } = await state.stripe.confirmSetup({
      elements: state.elements,
      redirect: "if_required", // cards never need to leave the page
      confirmParams: { return_url: window.location.href },
    });
    if (error) {
      // Declined, wrong number, failed 3-D Secure: Stripe's message is
      // written for the person typing.
      $("card-error").textContent = error.message;
      return;
    }

    closeCardForm();
    const cards = await loadCards();
    // The first card becomes the default, so invoices have a card to charge.
    if (cards && !cards.some((c) => c.is_default) && setupIntent.payment_method) {
      await api("PUT", billingPath(`/payment-methods/${encodeURIComponent(setupIntent.payment_method)}/default`));
      await loadCards();
    }
  } catch (err) {
    $("card-error").textContent = err.message;
  } finally {
    button.disabled = false;
  }
});

$("cancel-card").addEventListener("click", closeCardForm);

function closeCardForm() {
  if (state.elements) {
    const el = state.elements.getElement("payment");
    if (el) el.destroy();
    state.elements = null;
  }
  $("card-form").hidden = true;
  $("card-error").textContent = "";
}

// ---------- boot ----------

if (sessionStorage.getItem(TOKEN_KEY)) {
  start().catch(() => logout());
}
