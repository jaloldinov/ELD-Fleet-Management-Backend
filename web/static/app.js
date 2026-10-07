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
    this.body = body; // details of validation errors
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
  await loadSubscription();
}

// getStripe loads Stripe.js with our account's public key, once.
async function getStripe() {
  if (!state.stripe) {
    const cfg = await api("GET", "/billing/config");
    state.stripe = Stripe(cfg.publishable_key);
  }
  return state.stripe;
}

// money formats cents as dollars: 40000 -> "$400.00".
function money(cents) {
  return (cents / 100).toLocaleString("en-US", { style: "currency", currency: "USD" });
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
  loadSubscription();
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
    const stripe = await getStripe();
    const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    state.elements = stripe.elements({
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
    const { error, setupIntent } = await (await getStripe()).confirmSetup({
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

// ---------- subscription ----------

const sub = {
  products: [],     // the catalog (active products with active prices)
  trucks: 0,        // active trucks: the quantity of per-truck prices
  payment: null,    // an invoice waiting to be paid in the browser, if any
  current: null,    // the current subscription
  mode: "new",      // the picker: "new" (subscribe) or "change" (change plan)
  prorationDate: 0, // from the last preview: confirm the quote the user saw
  previewTimer: 0,
};

async function loadSubscription() {
  $("sub-current").hidden = true;
  $("sub-picker").hidden = true;
  $("sub-message").textContent = "";
  $("sub-error").textContent = "";
  if (!state.companyId) return;
  try {
    // POST refresh, not GET: it re-reads Stripe, and returns the payment
    // still due if an invoice is unpaid (e.g. the tab was closed).
    showSubscription(await api("POST", billingPath("/subscription/refresh")));
  } catch (err) {
    if (err.status === 404 && err.message === "route not found") {
      $("sub-message").textContent = "Billing is not enabled on this server.";
    } else if (err.status === 404) {
      await showPicker("new"); // no subscription yet
    } else {
      $("sub-message").textContent = `Could not load the subscription: ${err.message}`;
    }
  }
}

function showSubscription(st) {
  const s = st.subscription;
  sub.current = s;
  sub.payment = st.payment;

  const status = $("sub-status");
  status.textContent = s.status.replace("_", " ");
  status.className = "status " + (["active", "trialing"].includes(s.status) ? "good"
    : ["past_due", "unpaid", "incomplete"].includes(s.status) ? "bad" : "");

  const end = s.current_period_end ? new Date(s.current_period_end).toLocaleDateString() : "";
  $("sub-period").textContent = !end ? ""
    : s.cancel_at_period_end ? `Ends ${end}`
    : `${s.interval === "year" ? "Yearly" : "Monthly"}, renews ${end}`;

  const list = $("sub-items");
  list.replaceChildren();
  for (const it of s.items) {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = it.billing_unit === "truck"
      ? `${it.product_name}: ${it.quantity} truck${it.quantity === 1 ? "" : "s"} × ${money(it.unit_amount)}`
      : it.product_name;
    const amount = document.createElement("span");
    amount.className = "amount";
    amount.textContent = money(it.amount);
    li.append(name, amount);
    list.append(li);
  }
  $("sub-total").textContent = `${money(s.total_amount)} / ${s.interval}`;

  const running = ["active", "trialing"].includes(s.status);
  const pay = $("sub-pay");
  pay.hidden = !sub.payment;
  if (sub.payment) pay.textContent = `Pay ${money(sub.payment.amount_due)} now`;
  $("sub-change").hidden = !running || !!sub.payment;
  $("sub-cancel").hidden = !(running || s.status === "past_due") || s.cancel_at_period_end;
  $("sub-resume").hidden = !s.cancel_at_period_end;

  const messages = {
    incomplete: "Not active yet: the first invoice is waiting for payment.",
    past_due: "A payment failed. Stripe will retry the card over the next days; " +
      "add a working card and make it the default to avoid suspension.",
    unpaid: "Suspended: payments failed. Trucks, drivers and ELD devices are locked until the open invoice is paid.",
  };
  $("sub-message").textContent = sub.payment && running
    ? "A plan change is waiting for payment; the current plan stays until it is paid."
    : s.cancel_at_period_end ? `Cancelled: the service continues until ${end}.`
    : messages[s.status] || "";
  $("sub-current").hidden = false;
}

// showPicker shows the plan list: to subscribe ("new") or to change the
// current plan ("change", with a live quote from Stripe).
async function showPicker(mode) {
  const [catalog, trucks] = await Promise.all([
    api("GET", "/products?page_size=100"),
    api("GET", `/trucks?is_active=true&page_size=1&company_id=${encodeURIComponent(state.companyId)}`),
  ]);
  sub.products = catalog.items;
  sub.trucks = trucks.total;
  sub.mode = mode;

  const changing = mode === "change";
  $("subscribe").textContent = changing ? "Confirm change" : "Subscribe and pay";
  $("plan-back").hidden = !changing;
  $("plan-total-line").hidden = changing;
  $("plan-preview").hidden = true;
  $("plan-error").textContent = "";
  if (changing) {
    $("sub-picker").interval.value = sub.current.interval;
  }
  renderPlans();
  if (changing) {
    // Start from the current plan: the user ticks what they want after the change.
    const current = new Set(sub.current.items.map((it) => it.price_id));
    for (const box of $("plan-list").querySelectorAll("input")) box.checked = current.has(box.value);
    updatePlanTotal();
  }
  $("sub-picker").hidden = false;
  if (sub.products.length === 0) $("sub-message").textContent = "No plans are offered yet.";
}

// renderPlans lists, for the chosen interval, each product's price.
function renderPlans() {
  const interval = $("sub-picker").interval.value;
  const list = $("plan-list");
  list.replaceChildren();
  for (const product of sub.products) {
    const price = product.prices.find((p) => p.interval === interval);
    if (!price) continue; // no price for this interval

    const li = document.createElement("li");
    const label = document.createElement("label");
    const box = document.createElement("input");
    box.type = "checkbox";
    box.value = price.id;
    box.dataset.amount = price.billing_unit === "truck" ? price.unit_amount * sub.trucks : price.unit_amount;
    box.addEventListener("change", onPlanEdited);
    const text = document.createElement("span");
    text.textContent = price.billing_unit === "truck"
      ? `${product.name}: ${money(price.unit_amount)} per truck × ${sub.trucks} active truck${sub.trucks === 1 ? "" : "s"}`
      : `${product.name}: ${money(price.unit_amount)}`;
    label.append(box, text);

    const amount = document.createElement("span");
    amount.className = "amount";
    amount.textContent = money(Number(box.dataset.amount));
    li.append(label, amount);
    list.append(li);
  }
  updatePlanTotal();
}

function chosenPrices() {
  return [...$("plan-list").querySelectorAll("input:checked")].map((b) => b.value);
}

function updatePlanTotal() {
  let total = 0;
  for (const box of $("plan-list").querySelectorAll("input:checked")) total += Number(box.dataset.amount);
  $("plan-total").textContent = money(total);
}

function onPlanEdited() {
  updatePlanTotal();
  if (sub.mode !== "change") return;
  // Ask Stripe for a quote once the user stops clicking for a moment.
  clearTimeout(sub.previewTimer);
  sub.previewTimer = setTimeout(previewChange, 400);
}

for (const radio of document.querySelectorAll('#sub-picker input[name="interval"]')) {
  radio.addEventListener("change", () => {
    renderPlans();
    onPlanEdited();
  });
}

// previewChange shows what the change would cost right now.
async function previewChange() {
  const box = $("plan-preview");
  const lines = $("preview-lines");
  const summary = $("preview-summary");
  lines.replaceChildren();
  sub.prorationDate = 0;
  if (chosenPrices().length === 0) {
    box.hidden = true;
    return;
  }
  try {
    const pv = await api("POST", billingPath("/subscription/preview"), { price_ids: chosenPrices() });
    sub.prorationDate = pv.proration_date;
    for (const l of pv.lines) {
      const li = document.createElement("li");
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = l.description;
      const amount = document.createElement("span");
      amount.className = "amount";
      amount.textContent = money(l.amount);
      li.append(name, amount);
      lines.append(li);
    }
    summary.textContent = pv.amount_due > 0 ? `Charged now: ${money(pv.amount_due)}`
      : pv.credit > 0 ? `Credit for unused time: ${money(pv.credit)} (pays your next invoices)`
      : "Nothing to pay now";
  } catch (err) {
    const details = err.status === 400 ? Object.values(err.body?.error?.details || {}).join(" ") : err.message;
    summary.textContent = details;
  }
  box.hidden = false;
}

$("sub-change").addEventListener("click", () => showPicker("change").catch((err) => {
  $("sub-error").textContent = err.message;
}));

$("plan-back").addEventListener("click", () => {
  $("sub-picker").hidden = true;
});

$("sub-picker").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const button = $("subscribe");
  const priceIds = chosenPrices();
  $("plan-error").textContent = "";
  if (priceIds.length === 0) {
    $("plan-error").textContent = "Choose at least one plan.";
    return;
  }
  button.disabled = true;
  try {
    let st;
    if (sub.mode === "change") {
      // The difference is charged now (or credited), with the date of the
      // quote the user saw, so the charge equals the quote.
      const body = { price_ids: priceIds };
      if (sub.prorationDate) body.proration_date = sub.prorationDate;
      st = await api("PUT", billingPath("/subscription"), body);
    } else {
      // Our API creates the subscription in Stripe: "incomplete", with an
      // open first invoice. Nothing is charged yet.
      st = await api("POST", billingPath("/subscription"), { price_ids: priceIds });
    }
    $("sub-picker").hidden = true;
    showSubscription(st);
    // An invoice to pay in the browser: the first one, or a change for
    // which the bank asks for 3-D Secure.
    if (st.payment) await payNow();
  } catch (err) {
    const details = err.status === 400 ? " " + Object.values(err.body?.error?.details || {}).join(" ") : "";
    $("plan-error").textContent = err.message + details;
  } finally {
    button.disabled = false;
  }
});

$("sub-cancel").addEventListener("click", async () => {
  const end = sub.current.current_period_end ? new Date(sub.current.current_period_end).toLocaleDateString() : "the end of the period";
  if (!confirm(`Cancel the subscription? The service continues until ${end}, then stops. You can undo this until then.`)) return;
  try {
    const s = await api("POST", billingPath("/subscription/cancel"), { at_period_end: true });
    showSubscription({ subscription: s, payment: null });
  } catch (err) {
    $("sub-error").textContent = err.message;
  }
});

$("sub-resume").addEventListener("click", async () => {
  try {
    const s = await api("POST", billingPath("/subscription/resume"));
    showSubscription({ subscription: s, payment: null });
  } catch (err) {
    $("sub-error").textContent = err.message;
  }
});

$("sub-pay").addEventListener("click", payNow);
$("sub-refresh").addEventListener("click", loadSubscription);

// payNow pays the waiting invoice with the default card. Stripe.js shows
// the bank's 3-D Secure check if it asks for one; the card itself never
// passes through our server.
async function payNow() {
  const pay = $("sub-pay");
  pay.disabled = true;
  $("sub-error").textContent = "";
  try {
    const stripe = await getStripe();
    const { error } = await stripe.confirmPayment({
      clientSecret: sub.payment.client_secret,
      confirmParams: { payment_method: sub.payment.payment_method_id, return_url: window.location.href },
      redirect: "if_required",
    });
    if (error) {
      // Declined, insufficient funds, failed 3-D Secure: written for people.
      $("sub-error").textContent = error.message;
      return;
    }
    // Paid. Stripe applies the subscription (or the change) right after the
    // invoice is paid; ask a few times in case it takes a moment.
    for (let i = 0; i < 5; i++) {
      const st = await api("POST", billingPath("/subscription/refresh"));
      showSubscription(st);
      if (!st.payment) return;
      await new Promise((r) => setTimeout(r, 1000));
    }
  } catch (err) {
    $("sub-error").textContent = err.message;
  } finally {
    pay.disabled = false;
  }
}

// ---------- boot ----------

if (sessionStorage.getItem(TOKEN_KEY)) {
  start().catch(() => logout());
}
