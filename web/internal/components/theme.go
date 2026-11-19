package components

// themeBootScript is the tiny, dependency-free theme resolver injected at
// the very top of the app-shell <head> so the theme is applied before the
// stylesheet paints (no flash). It reads the persisted preference
// (light | dark | system; default light — the current look), resolves
// "system" against the OS color-scheme into an effective light/dark, and
// stamps both onto <html>:
//
//	data-theme       = effective theme the CSS keys on (light | dark)
//	data-theme-pref  = the user's chosen preference (drives the switcher's
//	                   active state, purely via CSS attribute selectors)
//
// It also exposes window.__kiffSetTheme(pref) for the topbar switcher,
// re-resolves when the OS scheme changes while on "system", and delegates
// clicks on [data-set-theme] elements. the web-stack design: a small
// vanilla script, no framework. A JS failure leaves the default light
// theme intact.
func themeBootScript() string {
	return `<script>
(function(){
  try{
    var K='kiff-theme';
    var mq=window.matchMedia('(prefers-color-scheme: dark)');
    function pref(){ try{ return localStorage.getItem(K)||'light'; }catch(e){ return 'light'; } }
    function apply(){
      var p=pref();
      var eff = p==='dark' ? 'dark' : (p==='light' ? 'light' : (mq.matches ? 'dark' : 'light'));
      var d=document.documentElement;
      d.setAttribute('data-theme', eff);
      d.setAttribute('data-theme-pref', p);
    }
    apply();
    window.__kiffSetTheme=function(p){ try{ localStorage.setItem(K,p); }catch(e){} apply(); };
    if(mq.addEventListener){ mq.addEventListener('change', function(){ if(pref()==='system') apply(); }); }
    document.addEventListener('click', function(e){
      var b = e.target.closest && e.target.closest('[data-set-theme]');
      if(b){ window.__kiffSetTheme(b.getAttribute('data-set-theme')); }
    });
  }catch(e){}
})();
</script>`
}
