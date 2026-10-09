/* Keep 公共工具（所有页面共用）：HTML 转义。 */
/* 集中定义避免 index/manage/stats/users 四处重复实现不一致。 */
function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, function (ch) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
    });
}
