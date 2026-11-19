(function () {
  'use strict';

  document.querySelectorAll('[data-demo-shell]').forEach(function (shell) {
    var launch = shell.querySelector('[data-demo-launch]');
    var poster = shell.querySelector('[data-demo-poster]');
    var stage = shell.querySelector('[data-demo-stage]');
    var frame = shell.querySelector('[data-demo-frame]');
    var close = shell.querySelector('[data-demo-close]');

    if (!launch || !poster || !stage || !frame || !close) return;

    launch.addEventListener('click', function (event) {
      event.preventDefault();
      if (!frame.getAttribute('src')) frame.setAttribute('src', frame.getAttribute('data-src'));
      poster.hidden = true;
      launch.hidden = true;
      stage.hidden = false;
      shell.setAttribute('data-demo-active', 'true');
      close.focus();
    });

    close.addEventListener('click', function () {
      stage.hidden = true;
      frame.removeAttribute('src');
      shell.removeAttribute('data-demo-active');
      poster.hidden = false;
      launch.hidden = false;
      launch.focus();
    });
  });
}());
