
const DEFAULT_PRESETS = {
    'default': { rgb: '139,148,158', hex: '#8b949e', name: '其他', is_low_freq: false }
};
let sourceColors = { ...DEFAULT_PRESETS };

function hexToRgb(hex) {
    if (!hex || !hex.startsWith('#')) return '139,148,158';
    const n = parseInt(hex.slice(1), 16);
    if (isNaN(n)) return '139,148,158';
    return `${(n>>16)&255},${(n>>8)&255},${n&255}`;
}

async function loadSourceColorsFromDB() {
    const map = { ...DEFAULT_PRESETS };
    try {
        const res = await fetch('/api/sources');
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        if (res.ok) {
            const list = await res.json();
            if (Array.isArray(list)) {
                list.forEach(e => {
                    if (!e.id) return;
                    map[e.id] = {
                        rgb: hexToRgb(e.color),
                        hex: e.color || '#8b949e',
                        name: e.name || e.id,
                        emoji: e.emoji || '📱',
                        is_low_freq: !!e.is_low_freq,
                        sort_order: (typeof e.sort_order === 'number') ? e.sort_order : 0
                    };
                });
            }
        }
    } catch (e) {
        console.warn('Failed to load sources from database');
    }
    sourceColors = map;
}

function getColor(id) {
    if (!sourceColors[id]) {
        let hash = 0;
        for (let i = 0; i < id.length; i++) {
            hash = id.charCodeAt(i) + ((hash << 5) - hash);
        }
        // 截断到 6 位 hex，避免 hash 超过 6 位产生非法颜色值
        const c = Math.abs(hash).toString(16).slice(0, 6);
        const hex = '#' + '000000'.substring(0, 6 - c.length) + c;
        sourceColors[id] = {
            rgb: hexToRgb(hex),
            hex: hex,
            name: id,
            emoji: '📱',
            is_low_freq: false
        };
    }
    return sourceColors[id];
}

// 年份范围校验（与后端 1900-9999 一致），防止 localStorage 损坏时产生非法日期渲染
let year = parseInt(localStorage.getItem('dashboard_year')) || new Date().getFullYear();
if (!(year >= 1900 && year <= 9999)) year = new Date().getFullYear();
let currentSource = localStorage.getItem('dashboard_source') || 'combined';
let combineMode = localStorage.getItem('dashboard_combine_mode') || 'winner';
let pendingTargetSource = null;
let allData = [];
let statsData = {};
let chart = null;

// 版本自检标记：部署后可在控制台输入 window.__KEEP_VER 确认加载的是否为最新修复版
window.__KEEP_VER = 'fix4-20261010';

// ============ 主题切换 ============
let themePref = localStorage.getItem('keep_theme') || 'system';

function currentTheme() {
    if (themePref === 'system') {
        return (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light';
    }
    return themePref;
}

function applyTheme() {
    const eff = currentTheme();
    document.documentElement.dataset.theme = eff;
    const m = document.querySelector('meta[name="theme-color"]');
    if (m) m.setAttribute('content', eff === 'dark' ? '#0d1117' : '#ffffff');
    // 主题变化需重建图表（ECharts 主题在 init 时生效）
    if (chart) {
        try { chart.dispose(); } catch (_) {}
        chart = null;
    }
    if (allData) renderHeatmap();
    updateThemeMenu();
}

function setThemePref(p) {
    themePref = p;
    localStorage.setItem('keep_theme', p);
    applyTheme();
}

function updateThemeMenu() {
    const items = document.querySelectorAll('.theme-menu-item');
    items.forEach(function (el) {
        el.classList.toggle('active', el.dataset.themeOpt === themePref);
    });
}

// 常驻星期列（一/三/五）：固定在热力图左侧不随滚动，行位严格对齐日历格子行
// 直接用 convertToPixel 取当年 1 月首个周一/周三/周五所在行的真实网格 y（与 ECharts 渲染一致）
function layoutWeekLabels() {
    const col = document.getElementById('hmWeekCol');
    const wrap = document.querySelector('.heatmap-wrap');
    const hm = document.getElementById('heatmap');
    if (!col || !wrap || !hm || !chart || chart.isDisposed()) return;
    const hmR = hm.getBoundingClientRect();
    const wr = wrap.getBoundingClientRect();
    const topOff = hmR.top - wr.top;
    const y = year || 2026;
    const jan1 = new Date(y, 0, 1);
    const monday = 1 + (((8 - jan1.getDay()) % 7) || 0);  // 1 月首个周一
    const rows = [monday, monday + 2, monday + 4];        // 周一 / 周三 / 周五
    col.querySelectorAll('.hm-week-item').forEach(function (el, i) {
        const dateStr = y + '-' + String(1).padStart(2, '0') + '-' + String(rows[i]).padStart(2, '0');
        const px = chart.convertToPixel({ coordSysName: 'calendar', calendarIndex: 0 }, dateStr);
        if (px && typeof px[1] === 'number') {
            el.style.top = (topOff + px[1]) + 'px';
        }
    });
}

// ============ 热力图横向进度条 ============
function syncScrollbar() {
    const track = document.getElementById('hmScrollbar');
    const thumb = document.getElementById('hmScrollThumb');
    const wrap = document.querySelector('.heatmap-wrap');
    if (!track || !thumb || !wrap) return;
    const maxScroll = wrap.scrollWidth - wrap.clientWidth;
    if (maxScroll <= 0) { track.style.display = 'none'; return; }
    track.style.display = 'block';
    const tw = track.clientWidth;
    const thw = thumb.offsetWidth;
    const p = wrap.scrollLeft / maxScroll;
    thumb.style.left = Math.round(p * (tw - thw)) + 'px';
}

// 需要滚动时，页面打开后的首次渲染把当前月份放到合适位置，不用手动滑动找：
// 前后内容都充足 → 当前月居中；当前月靠后（如 12 月，右侧内容不足半屏）→ 贴右缘，
// 左侧完整显示之前的月份，避免"当前月居中、右侧一大片空白"；靠前（如 1 月）→ 贴左。
// 返回 true 表示已成功定位（调用方据此决定不再重试）
function centerOnCurrentMonth(wrap, cell) {
    const scrollW = wrap.scrollWidth;
    const cw = wrap.clientWidth;
    const maxScroll = scrollW - cw;
    if (maxScroll <= 0) return false;
    const m = new Date().getMonth() + 1;
    const dateStr = year + '-' + String(m).padStart(2, '0') + '-01';
    const px = chart.convertToPixel({ coordSysName: 'calendar', calendarIndex: 0 }, dateStr);
    if (!px || typeof px[0] !== 'number') return false;
    const monthCols = weeksBetweenMonths(year, m, m);
    const midX = px[0] + (monthCols - 1) * cell / 2;
    let target;
    if (midX + cw / 2 > scrollW) {
        target = maxScroll; // 右侧内容不足半屏 → 当前月贴右缘
    } else if (midX < cw / 2) {
        target = 0; // 当前月太靠前 → 贴左
    } else {
        target = midX - cw / 2; // 前后都充足 → 居中
    }
    target = Math.max(0, Math.min(maxScroll, target));
    wrap.scrollLeft = target;
    syncScrollbar();
    layoutMonthLabels();
    return true;
}

// 月份标签覆盖层：标签贴在各自月份首日所在列的中心。
// 标签层绝对定位在滚动容器(.heatmap-wrap)内部，会随内容一起滚动，
// 因此 left 用内容坐标(px[0])即可与格子列天然对齐（不能再减 scrollLeft，否则双重偏移）；
// 隐藏判断用屏幕坐标（内容坐标 - scrollLeft），右缘被切掉或滚出可视区时隐藏，避免"4月露头"
function layoutMonthLabels() {
    const el = document.getElementById('hmMonthLabels');
    const wrap = document.querySelector('.heatmap-wrap');
    if (!el || !chart || chart.isDisposed()) return;
    if (!el.__spans) {
        el.__spans = [];
        for (let m = 1; m <= 12; m++) {
            const span = document.createElement('span');
            span.textContent = m + '月';
            el.appendChild(span);
            el.__spans.push(span);
        }
    }
    const cw = wrap.clientWidth;
    const sl = wrap.scrollLeft;
    // heatmap 在 wrap 内的布局偏移（内容不满时会被 justify-content 居中偏移）：
    // 标签层 absolute 钉在 wrap 左缘，必须加上该偏移才能与格子列同参考系
    const hmEl = document.getElementById('heatmap');
    const off = hmEl ? hmEl.offsetLeft : 0;
    for (let m = 1; m <= 12; m++) {
        const span = el.__spans[m - 1];
        const dateStr = year + '-' + String(m).padStart(2, '0') + '-01';
        const px = chart.convertToPixel({ coordSysName: 'calendar', calendarIndex: 0 }, dateStr);
        if (!px || typeof px[0] !== 'number') { span.style.visibility = 'hidden'; continue; }
        span.style.left = (px[0] + off) + 'px';
        // 标签需整体在可视区内才显示：中心离左缘不足半宽（或超出右缘）→ 隐藏，避免半截"月"字
        const hw = span.offsetWidth / 2;
        const sx = px[0] + off - sl;
        span.style.visibility = (sx < hw + 4 || sx > cw - hw - 4) ? 'hidden' : 'visible';
    }
}

// ============ 初始化 ============
async function loadMe() {
    try {
        const res = await fetch('/api/me');
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        if (res.ok) {
            window.__me = await res.json();
            renderFooterLinks();
        }
    } catch (_) {}
}

function renderFooterLinks() {
    const el = document.getElementById('footerLinks');
    const me = window.__me;
    if (!el || !me) return;
    let html = '';
    // 所有登录用户均可管理自己的事件类型；管理员额外显示用户管理
    html += '<a href="/manage.html">⚙️ 事件类型配置</a>';
    if (me.role === 'admin') {
        html += ' &nbsp;<a href="/users.html">👥 用户管理</a>';
    }
    if (me.username) {
        html += ' &nbsp;<span style="opacity:.75">' + escapeHtml(me.username) + '</span>';
        html += ' &nbsp;<a href="#" onclick="doLogout(event)">退出</a>';
    }
    el.innerHTML = html;
}

async function doLogout(e) {
    e.preventDefault();
    try { await fetch('/api/logout', { method: 'POST' }); } catch (_) {}
    window.location.href = '/login.html';
}

async function init() {
    // 四个数据请求互相独立，并行发出（替代原来串行 await，省掉 3 个网络往返）
    const pMe = loadMe();
    const pSrc = loadSourceColorsFromDB();
    const pStats = loadStats();
    const pData = loadData();
    const yearSel = document.getElementById('year');
    const cy = new Date().getFullYear();
    yearSel.innerHTML = '';
    for (let y = cy; y >= cy - 5; y--) {
        yearSel.innerHTML += `<option value="${y}" ${y===year?'selected':''}>${y}</option>`;
    }
    yearSel.onchange = async () => {
        year = parseInt(yearSel.value);
        localStorage.setItem('dashboard_year', year);
        // 切年后重新定位当前月（居中/贴边），避免停在旧年份的滚动位置
        window.__initedCentered = false;
        await loadData();
        render();
    };
    const modeSel = document.getElementById('combineMode');
    modeSel.value = combineMode;
    modeSel.onchange = () => {
        combineMode = modeSel.value;
        localStorage.setItem('dashboard_combine_mode', combineMode);
        renderHeatmap();
    };

    // 主题按钮与菜单
    const fab = document.getElementById('themeFab');
    const menu = document.getElementById('themeMenu');
    fab.addEventListener('click', function (e) {
        e.stopPropagation();
        menu.classList.toggle('show');
    });
    document.addEventListener('click', function (e) {
        if (!menu.contains(e.target) && !fab.contains(e.target)) menu.classList.remove('show');
    });
    menu.querySelectorAll('.theme-menu-item').forEach(function (el) {
        el.addEventListener('click', function () { setThemePref(el.dataset.themeOpt); menu.classList.remove('show'); });
    });
    // 跟随系统主题变化
    const mq = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)');
    const onSysChange = function () { if (themePref === 'system') applyTheme(); };
    if (mq && mq.addEventListener) mq.addEventListener('change', onSysChange);
    else if (mq && mq.addListener) mq.addListener(onSysChange);
    updateThemeMenu();

    await Promise.all([pMe, pSrc, pStats, pData]);
    render();
    // 数据与首帧画布就绪后再淡入，避免 F5 后"空状态→真实数据"逐个闪现/高度跳变
    requestAnimationFrame(() => { document.querySelector('.container').style.opacity = 1; });

    // 热力图横向滚动：进度条同步 + 拖拽/点击 + 滚轮横向
    const heatWrap = document.querySelector('.heatmap-wrap');
    const track = document.getElementById('hmScrollbar');
    const thumb = document.getElementById('hmScrollThumb');
    // 原生触摸兜底：触控设备触摸热力图任意位置立即隐藏小时间窗口（tooltip）
    if (heatWrap) {
        heatWrap.addEventListener('touchstart', function () {
            if (isTouchDevice() && chart && !chart.isDisposed()) {
                chart.dispatchAction({ type: 'hideTip' });
            }
        }, { passive: true });
    }
    if (heatWrap && track && thumb) {
        heatWrap.addEventListener('scroll', function () {
            if (window.__sbRaf) return;
            window.__sbRaf = requestAnimationFrame(function () {
                window.__sbRaf = 0;
                syncScrollbar();
                layoutMonthLabels();
            });
        });
        heatWrap.addEventListener('wheel', function (e) {
            if (heatWrap.scrollWidth <= heatWrap.clientWidth) return;
            if (Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey) return;
            heatWrap.scrollLeft += e.deltaY;
            e.preventDefault();
        }, { passive: false });
        track.addEventListener('pointerdown', function (e) {
            const tw = track.clientWidth - thumb.offsetWidth;
            const wrap = heatWrap;
            if (e.target === thumb) {
                const startX = e.clientX;
                const startLeft = thumb.offsetLeft;
                const move = function (ev) {
                    let nl = startLeft + (ev.clientX - startX);
                    nl = Math.max(0, Math.min(tw, nl));
                    thumb.style.left = nl + 'px';
                    wrap.scrollLeft = (nl / tw) * (wrap.scrollWidth - wrap.clientWidth);
                };
                const up = function () {
                    document.removeEventListener('pointermove', move);
                    document.removeEventListener('pointerup', up);
                };
                document.addEventListener('pointermove', move);
                document.addEventListener('pointerup', up);
            } else {
                const rect = track.getBoundingClientRect();
                let nl = e.clientX - rect.left - thumb.offsetWidth / 2;
                nl = Math.max(0, Math.min(tw, nl));
                thumb.style.left = nl + 'px';
                wrap.scrollLeft = (nl / tw) * (wrap.scrollWidth - wrap.clientWidth);
            }
            e.preventDefault();
        });
    }

    // 详情弹窗只能点右上角 × 关闭（点击遮罩/滑动屏幕不会关闭它）；
    // 点击遮罩仅隐藏小提示框（日期+次数），详情大页保持打开
    const detailOv = document.getElementById('detailModal');
    detailOv.addEventListener('click', function (e) {
        if (e.target === detailOv) {
            if (chart && !chart.isDisposed()) chart.dispatchAction({ type: 'hideTip' });
        }
        e.stopPropagation();
    });

    // PC 端：点击热力图之外的位置隐藏悬停提示
    document.addEventListener('click', function (e) {
        if (!(e.target && e.target.closest && e.target.closest('#heatmap'))) {
            if (chart && !chart.isDisposed()) chart.dispatchAction({ type: 'hideTip' });
        }
    });

    // 30 秒轮询：页面不可见时暂停，恢复时立即刷新一次（避免后台标签页空转拉取）
    let pollTimer = null;
    async function pollNow() {
        await loadSourceColorsFromDB();
        await loadStats();
        await loadData();
        render();
    }
    function startPolling() {
        if (pollTimer) return;
        pollTimer = setInterval(pollNow, 30000);
    }
    function stopPolling() {
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
    }
    document.addEventListener('visibilitychange', function () {
        if (document.hidden) {
            stopPolling();
        } else {
            startPolling();
            pollNow();
        }
    });
    startPolling();
}

async function loadStats() {
    try {
        const res = await fetch('/api/stats');
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        statsData = await res.json();
    } catch (_) {
        statsData = {};
    }
}

async function loadData() {
    try {
        // fields 裁剪：主页只用 id/source/context/timestamp/local_date，
        // 不拉取最大 8KB/条的 metadata，显著减小响应体
        const res = await fetch(`/api/contributions?year=${year}&fields=id,source,context,timestamp,local_date`);
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        allData = await res.json() || [];
    } catch (_) {
        allData = [];
    }
}

function handleRecordClick() {
    if (currentSource === 'combined') openSelectEventModal();
    else promptConfirmLog(currentSource);
}

function openSelectEventModal() {
    const container = document.getElementById('eventScrollContainer');
    const keys = Object.keys(sourceColors).filter(k => k !== 'default');
    if (keys.length === 0) {
        container.innerHTML = '<div style="color:var(--muted);padding:20px;grid-column:span 3;text-align:center;">暂无可用事件，请先在事件配置页面添加。</div>';
    } else {
        // 事件 id 通过 data-* 属性携带 + 事件委托绑定：避免把 id 拼进内联 onclick
        // 造成 JS 上下文注入（HTML 实体转义无法防护 onclick 里的引号注入）
        let html = '';
        keys.forEach(id => {
            const info = getColor(id);
            html += `<div class="event-card" data-eid="${escapeHtml(id)}">
    <span class="emoji">${escapeHtml(info.emoji || '📱')}</span>
    <span class="name">${escapeHtml((info.name || id).slice(0, 16))}</span>
</div>`;
        });
        container.innerHTML = html;
        container.querySelectorAll('.event-card').forEach(function (el) {
            el.addEventListener('click', function () { directLog(el.dataset.eid); });
        });
    }
    document.getElementById('selectModal').classList.add('show');
}

function closeSelectModal() {
    document.getElementById('selectModal').classList.remove('show');
}

function promptConfirmLog(sourceId) {
    pendingTargetSource = sourceId;
    const info = getColor(sourceId);
    document.getElementById('targetEventName').textContent = info.name || sourceId;
    const note = document.getElementById('noteInput');
    if (note) note.value = '';
    closeSelectModal();
    document.getElementById('confirmModal').classList.add('show');
}

function closeConfirmModal() {
    document.getElementById('confirmModal').classList.remove('show');
    pendingTargetSource = null;
}

// 全部视图一键记录：点击事件卡片后直接提交，不弹确认框、不填备注（context 固定 manual）
let directLogBusy = false;
async function directLog(sourceId) {
    if (directLogBusy) return;
    directLogBusy = true;
    closeSelectModal();
    try {
        const res = await fetch('/api/contributions', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify([{
                source: sourceId,
                context: 'manual',
                timestamp: new Date().toISOString(),
                metadata: { device: 'web', manual: true }
            }])
        });
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        if (res.ok) {
            showToast('✅ 记录成功！');
            await loadStats();
            await loadData();
            render();
        } else showToast('❌ 记录失败，请重试');
    } catch (e) {
        showToast('❌ 网络请求失败');
    } finally {
        directLogBusy = false;
    }
}

async function submitLog() {
    if (!pendingTargetSource) return;
    const target = pendingTargetSource;
    // 备注可选：留空则 context 为 manual（详情页不显示）；填了就作为 context 展示在时间与删除之间
    const note = (document.getElementById('noteInput').value || '').trim().slice(0, 10);
    closeConfirmModal();
    try {
        const res = await fetch('/api/contributions', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify([{
                source: target,
                context: note || 'manual',
                timestamp: new Date().toISOString(),
                metadata: { device: 'web', manual: true }
            }])
        });
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        if (res.ok) {
            showToast('✅ 记录成功！');
            await loadStats();
            await loadData();
            render();
        } else showToast('❌ 记录失败，请重试');
    } catch (e) {
        showToast('❌ 网络请求失败');
    }
}

function showToast(msg) {
    const toast = document.getElementById('toast');
    toast.textContent = msg;
    toast.classList.add('show');
    setTimeout(() => toast.classList.remove('show'), 2000);
}

// escapeHtml 由公共文件 static/common.js 提供（所有页面共用同一实现）

function fmtTime(t) {
    // 屏蔽秒钟：一律只显示 HH:mm
    if (!t) return '';
    if (t.length >= 16) return t.slice(11, 16);
    return t;
}

// 触控端判定：触控设备不显示悬停 tooltip（点击格子直接弹详情）。
// 除 touch 事件/maxTouchPoints 外，补充 (any-pointer: coarse) 与 UA 兜底，
// 避免个别浏览器未暴露 ontouchstart 时误判为 PC 而在移动端弹出小时间窗口。
function isTouchDevice() {
    return ('ontouchstart' in window) || (navigator.maxTouchPoints || 0) > 0 ||
        (window.matchMedia && window.matchMedia('(any-pointer: coarse)').matches) ||
        /Android|iPhone|iPad|iPod|Mobile|Windows Phone/i.test(navigator.userAgent);
}

// 计算某月区间跨过的周列数（以周一为一周起点），用于自适应格子尺寸
function weeksBetweenMonths(y, s, e) {
    const start = new Date(y, s - 1, 1);
    const end = new Date(y, e, 0);
    const msPerDay = 86400000;
    const monday = new Date(start);
    monday.setDate(start.getDate() - ((start.getDay() + 6) % 7));
    const sunday = new Date(end);
    sunday.setDate(end.getDate() + ((7 - end.getDay()) % 7));
    return Math.floor((sunday - monday) / msPerDay / 7) + 1;
}

let detailDate = null;
let detailRecords = [];
let pendingDeleteId = null;

// 格子点击处理（一次点击即弹窗）：
//  - 具体事件视图：点击有记录的格子直接弹出时间记录窗口；无记录给提示
//  - 全部视图：给出操作提示（不静默）
//  - 命中容错：params.data 为空（点到边框/间隙/空白）时用 convertFromPixel 反查格子
function handleCellTap(data, params) {
    if (currentSource === 'combined') {
        showToast('请先选择具体事件，再点击格子查看记录');
        return;
    }
    let date = data && data.value && data.value[0];
    if (!date && chart && !chart.isDisposed() && params && params.event) {
        try {
            const pt = chart.convertFromPixel(
                { coordSysName: 'calendar', calendarIndex: 0 },
                [params.event.offsetX, params.event.offsetY]
            );
            if (pt && pt[0]) date = pt[0];
        } catch (_) { /* 点击日历区域外，忽略 */ }
    }
    if (!date) return;
    const recs = (allData || []).filter(function (c) {
        return c.source === currentSource &&
            (c.local_date || (c.timestamp || '').slice(0, 10)) === date;
    });
    if (recs.length === 0) { showToast('当天没有记录'); return; }
    detailDate = date;
    detailRecords = recs.slice().sort(function (a, b) {
        return (a.timestamp || '').localeCompare(b.timestamp || '');
    });
    openDetailModal();
}

function openDetailModal() {
    // 弹窗打开时隐藏悬停提示，避免 PC 上弹窗背后残留小时间窗口
    if (chart && !chart.isDisposed()) chart.dispatchAction({ type: 'hideTip' });
    const info = getColor(currentSource);
    const name = escapeHtml(info.name || currentSource);
    document.getElementById('detailTitle').textContent = '「' + name + '」 ' + detailDate + ' 记录';
    renderDetailList();
    document.getElementById('detailModal').classList.add('show');
}

function renderDetailList() {
    const wrap = document.getElementById('detailList');
    if (!detailRecords.length) {
        wrap.innerHTML = '<div class="detail-empty">当天没有记录</div>';
        return;
    }
    wrap.innerHTML = detailRecords.map(function (c) {
        const hm = fmtTime(c.timestamp);
        const ctx = (c.context && c.context !== 'manual') ? c.context : '';
        return '<div class="detail-row">' +
            '<span class="detail-time">' + hm + '</span>' +
            '<span class="detail-ctx">' + escapeHtml(ctx) + '</span>' +
            '<button class="detail-del" onclick="requestDelete(' + c.id + ')">删除</button>' +
            '</div>';
    }).join('');
}

function closeDetailModal() {
    document.getElementById('detailModal').classList.remove('show');
    document.getElementById('detailList').innerHTML = '';
    if (chart && !chart.isDisposed()) chart.dispatchAction({ type: 'hideTip' });
    detailDate = null;
    detailRecords = [];
    pendingDeleteId = null;
}


function requestDelete(id) {
    pendingDeleteId = id;
    let rec = null;
    detailRecords.forEach(function (c) { if (c.id === id) rec = c; });
    const hm = rec ? fmtTime(rec.timestamp) : '';
    const ctx = rec && rec.context && rec.context !== 'manual' ? '（' + rec.context + '）' : '';
    document.getElementById('delTargetDesc').textContent = (detailDate || '') + ' ' + hm + ctx;
    document.getElementById('delConfirmModal').classList.add('show');
}

function closeDelConfirmModal() {
    document.getElementById('delConfirmModal').classList.remove('show');
    pendingDeleteId = null;
}

async function confirmDelete() {
    const id = pendingDeleteId;
    if (id == null) return;
    closeDelConfirmModal();
    try {
        const res = await fetch('/api/contributions?id=' + encodeURIComponent(id), { method: 'DELETE' });
        if (res.status === 401) { window.location.href = '/login.html'; return; }
        if (res.ok) {
            allData = allData.filter(function (c) { return c.id !== id; });
            showToast('🗑️ 已删除');
            await loadStats();
            render();
            const recs = (allData || []).filter(function (c) {
                return c.source === currentSource &&
                    (c.local_date || (c.timestamp || '').slice(0, 10)) === detailDate;
            });
            detailRecords = recs.slice().sort(function (a, b) {
                return (a.timestamp || '').localeCompare(b.timestamp || '');
            });
            if (detailRecords.length === 0) { closeDetailModal(); return; }
            renderDetailList();
        } else {
            showToast('❌ 删除失败，请重试');
        }
    } catch (e) {
        showToast('❌ 网络请求失败');
    }
}

function render() {
    renderTabs();
    renderStats();
    renderHeatmap();
}

function renderTabs() {
    const bySource = {};
    allData.forEach(c => { bySource[c.source] = (bySource[c.source] || 0) + 1; });
    if (currentSource !== 'combined' && !bySource[currentSource]) {
        currentSource = 'combined';
        localStorage.setItem('dashboard_source', 'combined');
    }
    const tabs = document.getElementById('sourceTabs');
    let html = tabHtml('combined', '全部', allData.length, 'linear-gradient(135deg,#238636,#3b82f6)');
    // 按事件配置页的拖拽顺序（sort_order）排序，同序时按次数降序、id 升序，保证稳定
    const ids = Object.keys(bySource);
    ids.sort(function (a, b) {
        const sa = getColor(a).sort_order || 0;
        const sb = getColor(b).sort_order || 0;
        if (sa !== sb) return sa - sb;
        if (bySource[b] !== bySource[a]) return bySource[b] - bySource[a];
        return a.localeCompare(b);
    });
    ids.forEach(function (id) {
        const c = getColor(id);
        html += tabHtml(id, c.name, bySource[id], c.hex);
    });
    tabs.innerHTML = html;
    tabs.querySelectorAll('.source-tab').forEach(el => {
        el.onclick = () => {
            currentSource = el.dataset.id;
            localStorage.setItem('dashboard_source', currentSource);
            render();
        };
    });
}

function tabHtml(id, name, count, color) {
    const active = currentSource === id ? 'active' : '';
    // data-id 属性值必须转义：source 是自由文本，转义后 dataset.id 读取仍为原始值
    const safeId = escapeHtml(id);
    return `<button class="source-tab ${active}" data-id="${safeId}">
        <span class="dot" style="background:${safeStyleColor(color)}"></span>
        ${escapeHtml(name)}<span class="count">${count}</span>
    </button>`;
}

// 仅允许 #RRGGBB 或内部已知的 linear-gradient 样式色，堵住 style 注入
function safeStyleColor(color) {
    if (typeof color === 'string' && /^#[0-9a-fA-F]{6}$/.test(color)) return color;
    if (typeof color === 'string' && color.indexOf('linear-gradient') === 0) return color;
    return '#8b949e';
}

function renderStats() {
    // 口径说明：总计/事件类 = 当前所选年份视图；今日/连续 = 后端全局实时值
    //（避免跨年查看时"今日"恒为 0、与"连续"口径矛盾）
    let total;
    if (currentSource === 'combined') {
        total = allData.length;
    } else {
        total = allData.filter(c => c.source === currentSource).length;
    }
    const srcSet = {};
    allData.forEach(c => { if (c.source) srcSet[c.source] = 1; });
    document.getElementById('statTotal').textContent = total;
    document.getElementById('statToday').textContent = statsData.today || 0;
    document.getElementById('statStreak').textContent = statsData.current_streak || 0;
    document.getElementById('statSources').textContent = Object.keys(srcSet).length;
}

function renderHeatmap() {
    const isCombined = currentSource === 'combined';
    document.getElementById('combineMode').style.display = isCombined ? '' : 'none';
    document.getElementById('statsBtn').style.display = isCombined ? '' : 'none';
    const c = isCombined
        ? { hex: 'linear-gradient(135deg,#238636,#3b82f6)', name: '全部' }
        : getColor(currentSource);
    document.getElementById('badgeDot').style.background =
        typeof c.hex === 'string' && c.hex.startsWith('#') ? c.hex : '#238636';
    document.getElementById('badgeName').textContent = c.name || currentSource;

    const data = isCombined ? allData : allData.filter(x => x.source === currentSource);
    const days = {};
    data.forEach(item => {
        const dt = item.local_date || (item.timestamp || '').split('T')[0];
        if (!dt) return;
        if (!days[dt]) days[dt] = { total: 0, src: {} };
        days[dt].total++;
        days[dt].src[item.source] = (days[dt].src[item.source] || 0) + 1;
    });

    // canvas 不支持 CSS 变量，取计算后的颜色值
    const cs = getComputedStyle(document.documentElement);
    const cv = (n, fb) => { const v = cs.getPropertyValue(n).trim(); return v || fb; };
    const cCell = cv('--cell', '#161b22');
    const cCellBorder = cv('--cell-border', '#0d1117');
    const cMuted = cv('--muted', '#8b949e');

    const chartData = Object.entries(days).map(([date, stats]) => {
        let color;
        if (isCombined) color = getCombinedColor(stats);
        else {
            const sc = getColor(currentSource);
            let opacity;
            if (sc.is_low_freq) {
                // 低频事件：1次=较多档(0.85)，2次及以上=最高档(1.0)
                opacity = stats.total >= 2 ? 1.0 : 0.85;
            } else {
                // 贴合图例四档亮度：1次=少、2次=中、3次=较多、4次及以上=多（最高亮度）
                const step = [0.35, 0.65, 0.85, 1.0];
                opacity = stats.total >= 4 ? 1.0 : step[Math.max(0, stats.total - 1)];
            }
            color = `rgba(${sc.rgb},${opacity})`;
        }
        return {
            value: [date, stats.total],
            itemStyle: { color, borderColor: cCellBorder, borderWidth: 1, borderRadius: 2 }
        };
    });

    // 把整年没记录的日子也补成计数 0 的格子：让空白格 hover 同样能显示日期（tooltip 只对 data 里的点触发）
    for (let m = 1; m <= 12; m++) {
        const daysInMonth = new Date(year, m, 0).getDate();
        for (let d = 1; d <= daysInMonth; d++) {
            const ds = year + '-' + String(m).padStart(2, '0') + '-' + String(d).padStart(2, '0');
            if (!days[ds]) {
                chartData.push({
                    value: [ds, 0],
                    itemStyle: { color: cCell, borderColor: cCellBorder, borderWidth: 1, borderRadius: 2 }
                });
            }
        }
    }

    const legendColor = isCombined ? { rgb: '35,134,54', hex: '#238636' } : getColor(currentSource);
    // 图例统一显示 无/少/中/较多/多
    const lv = ['无', '少', '中', '较多', '多'];
    document.getElementById('legend').innerHTML = `
        <div class="legend-item"><div class="legend-box" style="background:${cCell}"></div>${lv[0]}</div>
        <div class="legend-item"><div class="legend-box" style="background:rgba(${legendColor.rgb},0.35)"></div>${lv[1]}</div>
        <div class="legend-item"><div class="legend-box" style="background:rgba(${legendColor.rgb},0.65)"></div>${lv[2]}</div>
        <div class="legend-item"><div class="legend-box" style="background:rgba(${legendColor.rgb},0.85)"></div>${lv[3]}</div>
        <div class="legend-item"><div class="legend-box" style="background:${legendColor.hex}"></div>${lv[4]}</div>
    `;

    const el = document.getElementById('heatmap');
    const wrap = el.parentElement;
    const vw = window.innerWidth || 800;
    const vh = window.innerHeight || 600;
    const isNarrow = vw < 640;
    const isTablet = vw >= 640 && vw < 1024;

    // 自适应可见月数：手机≥3个月、平板约6个月、中屏约8个月、桌面(≥1280)完整12个月
    // 整年连续渲染不切断，超出可视区域时左右滑动 + 进度条查看
    const targetMonths = vw < 640 ? 3 : vw < 1024 ? 6 : vw < 1280 ? 8 : 12;
    // 滚动视图（手机/平板）减去"跨月边界列"（该列同时含上月末+下月初，
    // 完整显示它必然带出下月前几天），从而让 3/6 个月窗口内零露出下月内容
    const fullCols = weeksBetweenMonths(year, 1, targetMonths);
    const targetCols = vw < 1280 ? Math.max(1, fullCols - 1) : fullCols;
    // 按当年实际跨月周列数计算，避免固定 53 列在个别年份（跨年周对齐）被裁切
    const calCols = weeksBetweenMonths(year, 1, 12);
    const calRange = year.toString();

    void wrap.offsetWidth;
    let avail = wrap.clientWidth || vw;
    if (avail < 80) avail = vw - 24;

    const leftPad = 2;
    // 滚动视图（手机/平板）右缘贴边，让"跨月边界列"整列压在视口外；桌面完整展示留右边距
    const rightPad = vw < 1280 ? 0 : 24;
    let cell;
    if (vw < 1280) {
        // 小数格子：让前 targetCols 列恰好填满可视宽度，
        // 下一列（跨月边界列）整列压在右缘外，滚动窗口内零露出下月内容
        cell = (avail - leftPad - rightPad) / targetCols;
        cell = Math.max(10, Math.min(32, cell));
    } else {
        cell = Math.floor((avail - leftPad - rightPad) / targetCols);
        cell = Math.max(10, Math.min(32, cell));
    }

    const usedOutside =
        (document.querySelector('h1')?.offsetHeight || 40) +
        (document.getElementById('sourceTabs')?.offsetHeight || 48) +
        (document.querySelector('.stats')?.offsetHeight || 80) +
        (document.querySelector('.graph-header')?.offsetHeight || 40) +
        (document.getElementById('legend')?.offsetHeight || 28) +
        (document.querySelector('footer')?.offsetHeight || 28) + 80;

    const maxGraphH = Math.max(110, Math.min(vh - usedOutside, isNarrow ? 300 : (isTablet ? 260 : 340)));
    let graphH = Math.round(cell * 7 + (isNarrow ? 36 : 48));
    if (graphH > maxGraphH) {
        graphH = maxGraphH;
        const cellByH = Math.floor((graphH - (isNarrow ? 36 : 48)) / 7);
        if (cellByH > 0 && cellByH < cell) cell = Math.max(10, cellByH);
    }
    const graphW = calCols * cell + leftPad + rightPad;
    el.style.width = graphW + 'px';
    el.style.height = graphH + 'px';
    // 超出容器时左对齐以便横向滚动，未超出时居中
    wrap.style.justifyContent = graphW > avail ? 'flex-start' : 'center';

    const draw = () => {
        // 统一复用 ECharts 实例（旧实现为规避 iOS 渲染问题每次销毁重建，
        // 导致 30 秒轮询/窗口变化时整图重建、性能差且闪烁）
        const eTheme = (document.documentElement.dataset.theme === 'light') ? 'light' : 'dark';
        if (!chart) {
            chart = echarts.init(el, eTheme, { width: graphW, height: graphH, renderer: 'canvas' });
            // 点击判定（一次点击必弹窗）：
            //  1) mousedown/mouseup 位移 ≤10px 视为点击（手抖/点偏都算），绕过 zrender 的 drag 阈值
            //  2) 再兜底绑定原生 click（zrender 判定为点击时也会触发），350ms 内去重，绝不双弹
            //  移动端触摸时 zrender 同样派发模拟的 mousedown/mouseup/click，统一走这里
            let pressInfo = null;
            let lastTapAt = 0;
            const fireCellTap = function (data, params) {
                const now = Date.now();
                if (now - lastTapAt < 350) return; // mouseup 与 click 双触发时只处理一次
                lastTapAt = now;
                handleCellTap(data, params);
            };
            chart.on('mousedown', function (params) {
                // 触控设备：按下即隐藏小时间窗口（tooltip），点击后只弹详情
                if (isTouchDevice()) chart.dispatchAction({ type: 'hideTip' });
                pressInfo = {
                    x: params.event.offsetX,
                    y: params.event.offsetY,
                    data: params.data || null
                };
            });
            chart.on('mouseup', function (params) {
                if (!pressInfo) return;
                const dx = params.event.offsetX - pressInfo.x;
                const dy = params.event.offsetY - pressInfo.y;
                const dist = Math.sqrt(dx * dx + dy * dy);
                const pressedData = pressInfo.data; // 按下瞬间命中的格子数据
                pressInfo = null;
                if (dist > 10) return; // 位移过大视为拖动/滚动，不弹窗
                fireCellTap(params.data || pressedData, params);
            });
            chart.on('click', function (params) {
                fireCellTap(params.data || null, params);
            });
        } else {
            chart.resize({ width: graphW, height: graphH });
        }
        const labelSize = Math.max(9, Math.min(13, Math.floor(cell * 0.7)));
        const monthSize = Math.max(10, Math.min(13, Math.floor(cell * 0.75)));
        document.documentElement.style.setProperty('--month-label-size', monthSize + 'px');
        document.documentElement.style.setProperty('--week-label-size', labelSize + 'px');
        chart.setOption({
            backgroundColor: 'transparent',
            animation: false,
            tooltip: {
                show: !isTouchDevice(),  // 仅 PC 悬停显示；移动端点格子直接弹详情
                appendToBody: true,
                confine: true,
                triggerOn: 'mousemove',
                extraCssText: 'z-index: 9999; pointer-events: none;',
                position: function (point, params, dom, rect, size) {
                    const tipW = size.contentSize[0];
                    const tipH = size.contentSize[1];
                    // 返回图表内坐标；换算成屏幕坐标后双向钳位，保证 tooltip 完整在视口内
                    const hmLeft = document.getElementById('heatmap').getBoundingClientRect().left;
                    let y = point[1] - tipH - 12;
                    if (y < 10) y = point[1] + 12;
                    let screenX = point[0] + hmLeft;
                    // 右边放不下 → 翻到格子左边
                    if (screenX + tipW > window.innerWidth - 10) {
                        screenX = point[0] + hmLeft - tipW - 12;
                    }
                    // 左右双向钳位
                    if (screenX < 10) screenX = 10;
                    if (screenX + tipW > window.innerWidth - 10) screenX = window.innerWidth - 10 - tipW;
                    return [screenX - hmLeft, y];
                },
                formatter: p => {
                    if (!p.data || !p.data.value) return '';
                    const date = p.data.value[0];
                    const count = p.data.value[1];
                    if (!count) return `<b>${date}</b>`;  // 没记录的日子只显示日期
                    const dayStats = days[date];
                    let tip = `<b>${date}</b><br/>${count} 次`;
                    if (isCombined && dayStats) {
                        tip += '<br/>';
                        Object.entries(dayStats.src).sort((a, b) => b[1] - a[1]).forEach(([src, cnt]) => {
                            const info = getColor(src);
                            tip += `<br/><span style="color:${info.hex}">●</span> ${escapeHtml(info.name)}：${cnt}`;
                        });
                    }
                    return tip;
                }
            },
            calendar: {
                top: isNarrow ? 16 : Math.max(22, cell * 1.2),
                left: leftPad,
                right: rightPad,
                cellSize: [cell, cell],
                range: calRange,
                itemStyle: { color: cCell, borderColor: cCellBorder, borderWidth: 1 },
                splitLine: { show: false },
                yearLabel: { show: false },
                dayLabel: { show: false, firstDay: 1 },  // 一周从周一开始（1=周一，"一"在最顶行）；一/三/五 由外部常驻星期列显示
                monthLabel: { show: false }  // 月份标签改用 HTML 覆盖层（可在边缘被切时隐藏）
            },
            series: { type: 'heatmap', coordinateSystem: 'calendar', data: chartData }
        }, true);
        // setOption 同步完成布局后，只做需要像素坐标的标签定位/滚动居中；
        // 不再二次 chart.resize（尺寸与 init 时一致，多余 resize 会触发首帧高度抖动）。
        requestAnimationFrame(() => {
            syncScrollbar();
            layoutMonthLabels();
            layoutWeekLabels();
            if (!window.__initedCentered) {
                if (centerOnCurrentMonth(wrap, cell)) window.__initedCentered = true;
            }
        });
    };
    draw();
}

function getCombinedColor(stats) {
    const sources = Object.entries(stats.src);
    // 贴合图例四档亮度：1次=少、2次=中、3次=较多、4次及以上=多（最高亮度）
    const step = [0.35, 0.65, 0.85, 1.0];
    const op = (t) => t >= 4 ? 1.0 : step[Math.max(0, t - 1)];
    if (combineMode === 'winner') {
        let winner = 'default', max = 0;
        sources.forEach(([src, cnt]) => { if (cnt > max) { max = cnt; winner = src; } });
        const sc = getColor(winner);
        const opacity = sc.is_low_freq
            ? (stats.total >= 2 ? 1.0 : 0.85)
            : op(stats.total);
        return `rgba(${sc.rgb},${opacity})`;
    }
    // 总计：亮度按 0/1-5/6-10/11-15/16+ 分档（独立于普通 0/1/2/3/4），0 次空白
    const totalStep = (t) => t <= 0 ? 0 : (t <= 5 ? 0.35 : (t <= 10 ? 0.65 : (t <= 15 ? 0.85 : 1.0)));
    return `rgba(35,134,54,${totalStep(stats.total)})`;
}

window.addEventListener('resize', () => {
    clearTimeout(window.__rz);
    window.__rz = setTimeout(() => { if (allData) renderHeatmap(); }, 200);
});
window.addEventListener('orientationchange', () => {
    setTimeout(() => { if (allData) renderHeatmap(); }, 300);
});

if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => {
        navigator.serviceWorker.register('/sw.js').then(() => {
            console.log('[PWA] Service Worker registered');
        }).catch((err) => {
            console.error('[PWA] Service Worker registration failed:', err);
        });
    });
}

init();

