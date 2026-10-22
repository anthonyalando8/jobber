(() => {
 const container = document.getElementById('dashboard-snapshot');
 const status = document.getElementById('refresh-status');
 let refreshing = false;
 async function refresh() {
  if (refreshing || document.hidden) return;
  refreshing = true;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  const focus = document.activeElement;
  const href = container.contains(focus) ? focus.getAttribute('href') : null;
  const detailsOpen = Array.from(container.querySelectorAll('details')).map(d => d.open);
  try {
   const response = await fetch('/dashboard/snapshot', {cache:'no-store',signal:controller.signal});
   // The session expired or was revoked: go back to sign-in rather than polling a 401 forever.
   if (response.status === 401) { window.location.assign('/login?next=%2F'); return; }
   if (!response.ok) throw new Error('Snapshot unavailable');
   const html = await response.text();
   container.innerHTML = html;
   container.querySelectorAll('details').forEach((d,i) => { if (i < detailsOpen.length) d.open = detailsOpen[i]; });
   if (href) Array.from(container.querySelectorAll('a')).find(a => a.getAttribute('href') === href)?.focus({preventScroll:true});
   status.textContent = 'Live \u00b7 updated ' + new Date().toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'});
   const hour = new Date().getHours();
   document.getElementById('greeting').textContent = 'Good ' + (hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening') + ', Anthony';
  } catch (error) {
   status.textContent = 'Live update unavailable \u00b7 showing last snapshot';
  } finally { clearTimeout(timeout); refreshing = false; }
 }
 document.getElementById('refresh-button').addEventListener('click', refresh);
 document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
 setInterval(refresh, 20000);
 const enrich = document.getElementById('enrich-button');
 enrich.addEventListener('click', async () => {
  const message = document.getElementById('action-status');
  message.hidden = false; message.textContent = 'Researching new companies. This may take a few minutes.';
  enrich.disabled = true;
  try {
   const response = await fetch('/admin/enrich', {method:'POST'});
   // Do not echo raw upstream error text into the application.
   message.textContent = response.ok ? 'Research batch completed. Counts will update shortly.' : 'Research could not complete. Check recent activity and company details.';
   await refresh();
  } catch (error) { message.textContent = 'Research connection interrupted. Check activity before starting another batch.'; }
  finally { enrich.disabled = false; }
 });
 refresh();
})();
