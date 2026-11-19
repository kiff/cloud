(function () {
  'use strict';

  var memoryStore = {};
  function storeGet(key) {
    try { return sessionStorage.getItem(key); } catch (error) { return memoryStore[key] || null; }
  }
  function storeSet(key, value) {
    memoryStore[key] = String(value);
    try { sessionStorage.setItem(key, String(value)); } catch (error) {}
  }
  function storeRemove(key) {
    delete memoryStore[key];
    try { sessionStorage.removeItem(key); } catch (error) {}
  }

  var themeTouchedKey = 'kiff-demo-theme-touched';
  if (storeGet(themeTouchedKey) !== '1') {
    document.documentElement.setAttribute('data-theme', 'dark');
    document.documentElement.setAttribute('data-theme-pref', 'dark');
  }
  document.addEventListener('click', function (event) {
    if (event.target.closest('[data-set-theme]')) storeSet(themeTouchedKey, '1');
  });

  var steps = [
    { path: '/demo/cards', title: 'Your agents and their cards', copy: 'Each card lets one agent do one kind of job, such as refunding orders, up to a limit its owner set. This is the real Cards screen.', selector: '.dash-page-head' },
    { path: '/demo/cards/card-refunds-eu', title: 'One card, and what is left on it', copy: 'The card shows its ceiling, what has been drawn today, and how to connect it to the agent. Change the limit or revoke it from here.', selector: '.ac-overview, .dash-page-head' },
    { path: '/demo/dashboard/domains', title: 'The whole workspace', copy: 'This is the real Domains screen. Open either domain, inspect its lifecycle, and move freely through the application.', selector: '.dash-page-head' },
    { path: '/demo/dashboard/runtimes', title: 'The agents already connected', copy: 'KIFF Guard makes different frameworks visible through one runtime inventory. The records here are fixture data rendered by the real Runtimes page.', selector: '.dash-page-head' },
    { path: '/demo/dashboard/build?domain=accounts-payable', title: 'Build the shared contract', copy: 'Studio edits the same domain every agent will use: states, valid actions, parameters, authority, and execution boundaries.', selector: '.studio-shell, .dash-page-head' },
    { path: '/demo/dashboard/govern?control_id=ctrl-ap-pay', title: 'Operate the boundary', copy: 'Govern shows the active control and the production posture. Nothing here is a separate tour mock.', selector: '.cr-shell, .dash-page-head' },
    { path: '/demo/dashboard/domains/accounts-payable/evidence?tab=decisions', title: 'One decision history', copy: 'Allowed, blocked, and approval-required outcomes come from one operational record, regardless of which runtime proposed them.', selector: '.cr-shell, .dash-page-head' },
    { path: '/demo/dashboard/domains/accounts-payable/evidence?tab=approvals', title: 'Human authority stays explicit', copy: 'Review the exact request, actor, state, amount, and reason before an action can continue.', selector: '.cr-shell, .dash-page-head' },
    { path: '/demo/dashboard/receipts/trace-pay-3420', title: 'The proof survives the agent', copy: 'The real receipt screen closes the loop with a signed record that can be inspected independently of the proposing model.', selector: '.receipt, .dash-page-head' }
  ];

  var query = new URLSearchParams(window.location.search);
  var key = 'kiff-demo-tour-step';
  var activeKey = 'kiff-demo-tour-active';
  if (query.get('tour') === '1') storeSet(activeKey, '1');

  var badge = document.createElement('div');
  badge.className = 'demo-fixture-badge';
  badge.textContent = 'Interactive demo · sample data';
  document.body.appendChild(badge);

  var panel = document.createElement('aside');
  panel.className = 'demo-tour';
  panel.setAttribute('aria-label', 'Guided product tour');
  panel.innerHTML = '<div class="demo-tour-head"><span class="demo-tour-kicker"></span><button class="demo-tour-close" type="button" aria-label="Close guided tour">×</button></div><div class="demo-tour-body"><h2></h2><p></p></div><div class="demo-tour-actions"><button type="button" data-tour-back>Back</button><span class="demo-tour-progress"><i></i></span><button type="button" data-tour-next>Next</button></div>';
  document.body.appendChild(panel);

  var launch = document.createElement('button');
  launch.className = 'demo-tour-launch';
  launch.type = 'button';
  launch.textContent = 'Guided tour';
  launch.hidden = true;
  document.body.appendChild(launch);

  var currentPath = window.location.pathname;
  var requestedStep = query.get('step');
  var index = requestedStep === null ? Number(storeGet(key)) : Number(requestedStep);
  if (!Number.isFinite(index) || index < 0 || index >= steps.length) {
    index = steps.findIndex(function (step) { return currentPath === step.path.split('?')[0]; });
    if (index < 0) index = 0;
  }

  function addTourParam(path, step) {
    return path + (path.indexOf('?') === -1 ? '?' : '&') + 'tour=1&step=' + step;
  }

  function clearFocus() {
    document.querySelectorAll('.demo-tour-focus').forEach(function (node) { node.classList.remove('demo-tour-focus'); });
  }

  function render() {
    clearFocus();
    var step = steps[index];
    panel.querySelector('.demo-tour-kicker').textContent = 'Guided tour · ' + (index + 1) + ' of ' + steps.length;
    panel.querySelector('h2').textContent = step.title;
    panel.querySelector('p').textContent = step.copy;
    panel.querySelector('.demo-tour-progress i').style.width = (((index + 1) / steps.length) * 100) + '%';
    panel.querySelector('[data-tour-back]').disabled = index === 0;
    panel.querySelector('[data-tour-next]').textContent = index === steps.length - 1 ? 'Restart' : 'Next';
    var focus = document.querySelector(step.selector);
    if (focus) focus.classList.add('demo-tour-focus');
    storeSet(key, String(index));
  }

  function go(next) {
    index = next;
    storeSet(key, String(index));
    window.location.href = addTourParam(steps[index].path, index);
  }

  panel.querySelector('[data-tour-back]').addEventListener('click', function () { if (index > 0) go(index - 1); });
  panel.querySelector('[data-tour-next]').addEventListener('click', function () { go(index === steps.length - 1 ? 0 : index + 1); });
  panel.querySelector('.demo-tour-close').addEventListener('click', function () {
    storeRemove(activeKey);
    panel.hidden = true;
    launch.hidden = false;
    clearFocus();
  });
  launch.addEventListener('click', function () {
    storeSet(activeKey, '1');
    panel.hidden = false;
    launch.hidden = true;
    render();
  });

  document.querySelectorAll('[data-demo-sign-out]').forEach(function (form) {
    form.addEventListener('submit', function (event) { event.preventDefault(); });
  });

  if (storeGet(activeKey) === '1') {
    panel.hidden = false;
    launch.hidden = true;
    render();
  } else {
    panel.hidden = true;
    launch.hidden = false;
  }
}());
