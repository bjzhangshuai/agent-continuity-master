const vscode = require('vscode');
const cp = require('child_process');
const path = require('path');

function workspace() {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) throw new Error('请先打开已启用 Agent Continuity 的工作区');
  return folder.uri.fsPath;
}
function run(context, command, args = []) {
  const root = workspace();
  const python = vscode.workspace.getConfiguration('agentContinuity').get('python') || 'python3';
  const bridge = path.join(context.extensionPath, 'bridge.py');
  return new Promise((resolve, reject) => cp.execFile(python, [bridge, '--workspace', root, command, ...args],
    {cwd: root, timeout: 120000, maxBuffer: 8 << 20}, (error, stdout, stderr) => {
      if (error) reject(new Error((stderr || stdout || error.message).trim()));
      else { try { resolve(JSON.parse(stdout)); } catch (_) { reject(new Error('本地桥接返回无效结果')); } }
    }));
}
async function taskPicker(context) {
  const tasks = await run(context, 'tasks');
  const item = await vscode.window.showQuickPick(tasks.map(t => ({label: t.title, description: t.task_id, task: t})), {placeHolder: '选择连续性任务'});
  return item?.task;
}
async function activate(context) {
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 20);
  status.text = '$(cloud-upload) Continuity'; status.command = 'agentContinuity.status'; status.tooltip = '查看对话同步、MinIO 与账单状态'; status.show();
  context.subscriptions.push(status);
  const register = (name, action) => context.subscriptions.push(vscode.commands.registerCommand(name, async () => {
    try { await action(); } catch (error) { vscode.window.showErrorMessage('Agent Continuity: ' + error.message); }
  }));
  register('agentContinuity.status', async () => {
    const value = await run(context, 'status');
    vscode.window.showInformationMessage(`设备 ${value.device_name} · ${value.sessions.length} 个会话 · ${value.pending} 条待同步`);
  });
  register('agentContinuity.openConsole', async () => {
    const value = await run(context, 'status'); await vscode.env.openExternal(vscode.Uri.parse(value.console_url));
  });
  register('agentContinuity.resume', async () => {
    const task = await taskPicker(context); if (!task) return;
    const value = await run(context, 'resume', [task.task_id]);
    const document = await vscode.workspace.openTextDocument(value.file); await vscode.window.showTextDocument(document);
    vscode.window.showInformationMessage('已生成可审阅的跨设备上下文；新会话启动时也会注入该上下文。');
  });
  register('agentContinuity.upload', async () => {
    const task = await taskPicker(context); if (!task) return;
    const selected = await vscode.window.showOpenDialog({canSelectMany: false, defaultUri: vscode.Uri.file(workspace()), openLabel: '明确上传所选文件'});
    if (!selected?.length) return;
    const value = await run(context, 'upload', [task.task_id, selected[0].fsPath]);
    vscode.window.showInformationMessage(`已受理上传 ${value.artifact_id || ''}；云端持久化状态请在控制台确认。`);
  });
  register('agentContinuity.billing', async () => {
    const billing = await run(context, 'billing');
    const docs = billing.invoices || [];
    const items = docs.map(d => ({label: `${d.invoice.number} · ${d.invoice.status}`, description: `${d.invoice.currency} ${(d.invoice.amount_minor / 100).toFixed(2)}`, document: d}));
    items.push({label: '$(link-external) 打开完整账单页面', description: '用量、明细、回执与模拟支付'});
    const item = await vscode.window.showQuickPick(items, {placeHolder: '本地实验不会产生真实扣款'});
    if (!item) return;
    if (!item.document) return vscode.env.openExternal(vscode.Uri.parse(billing.console_url));
    const invoice = item.document.invoice;
    if (invoice.status !== 'open' || !invoice.test_mode) return vscode.env.openExternal(vscode.Uri.parse(billing.console_url));
    const yes = await vscode.window.showWarningMessage('仅创建“微信支付交互模拟”付费链接，不会调用微信或扣款。继续？', {modal: true}, '创建模拟链接');
    if (!yes) return;
    const checkout = await run(context, 'checkout', [invoice.invoice_id]);
    await vscode.env.openExternal(vscode.Uri.parse(checkout.payment_link));
  });
}
function deactivate() {}
module.exports = {activate, deactivate};
