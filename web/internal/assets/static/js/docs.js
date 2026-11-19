/* docs.js — copy-to-clipboard for code blocks in .prose, and for any
   pre[data-copy] (the /builders quick start prompts). */

(function () {
  'use strict';

  function addCopyButton(pre) {
    var btn = document.createElement('button');
    btn.className = 'prose-copy-btn';
    btn.setAttribute('aria-label', 'Copy code');
    btn.textContent = 'copy';
    btn.type = 'button';

    btn.addEventListener('click', function () {
      // Read the block without the button: its label must never be copied.
      var code = pre.querySelector('code');
      var text = code ? code.textContent : Array.prototype.filter.call(pre.childNodes, function (n) { return n !== btn; }).map(function (n) { return n.textContent; }).join('');

      navigator.clipboard.writeText(text).then(function () {
        btn.textContent = 'copied!';
        btn.classList.add('copied');
        setTimeout(function () {
          btn.textContent = 'copy';
          btn.classList.remove('copied');
        }, 1800);
      }).catch(function () {
        /* fallback: select the text */
        var sel = window.getSelection();
        var range = document.createRange();
        range.selectNodeContents(code || pre);
        sel.removeAllRanges();
        sel.addRange(range);
      });
    });

    pre.appendChild(btn);
  }

  document.querySelectorAll('.prose pre, pre[data-copy]').forEach(addCopyButton);
})();
