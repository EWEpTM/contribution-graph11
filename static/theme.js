/* Keep 主题同步（所有页面共用）：初始化 + 跨页同步 + 跟随系统 */
(function () {
    function effectiveTheme() {
        var p = localStorage.getItem('keep_theme') || 'system';
        if (p === 'system') {
            return (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light';
        }
        return p;
    }
    function apply() {
        var eff = effectiveTheme();
        document.documentElement.dataset.theme = eff;
        var m = document.querySelector('meta[name="theme-color"]');
        if (m) m.setAttribute('content', eff === 'dark' ? '#0d1117' : '#ffffff');
    }
    apply();
    // 跨页面同步：主页面切换主题后其他页面实时跟随（storage 事件）
    window.addEventListener('storage', function (e) {
        if (e.key === 'keep_theme') apply();
    });
    // 系统主题变化（仅 system 模式跟随）
    var mq = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)');
    if (mq && mq.addEventListener) {
        mq.addEventListener('change', function () {
            if ((localStorage.getItem('keep_theme') || 'system') === 'system') apply();
        });
    }
})();
