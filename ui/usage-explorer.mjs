export const dimensions = ['model','provider','project','feature','mode','user'];

export function usageCSV(result) {
  const cell = value => /[",\r\n]/.test(value) ? '"'+value.replaceAll('"','""')+'"' : value;
  return [result.columns,...result.rows].map(row=>row.map(cell).join(',')).join('\r\n')+'\r\n';
}

export function usageFilename(query) { return `kilo-usage-${query.startDate}-${query.endDate}-${query.costSource}.csv`; }

export function createUsageExplorer(root, api) {
  const doc=root.ownerDocument;
  let language='en', scope=null, result=null, loading=false, message='', generation=0, initialized=false;
  const today=new Date(), start=new Date(today);start.setUTCDate(start.getUTCDate()-29);
  let draft={startDate:start.toISOString().slice(0,10),endDate:today.toISOString().slice(0,10),groupBy:['model'],costSource:'cost',models:'',providers:'',projects:'',features:'',modes:''};
  const L=(en,es)=>language==='es'?es:en;
  const labels=()=>({model:L('Model','Modelo'),provider:L('Provider','Proveedor'),project:L('Project','Proyecto'),feature:L('Feature','Función'),mode:L('Mode','Modo'),user:L('User','Usuario')});
  const make=(tag,text,className)=>{const node=doc.createElement(tag);if(text!==undefined)node.textContent=text;if(className)node.className=className;return node;};
  const note=text=>make('p',text,'field-help');
  function render() {
    root.replaceChildren();
    root.setAttribute('aria-label',L('Historical usage CSV','CSV de uso histórico'));
    root.append(make('h2',L('Historical usage · CSV','Uso histórico · CSV')),note(L('Retrieve your usage from Kilo, including dates before this proxy was installed. Rows are daily aggregates, grouped by up to three dimensions.','Recupera tu uso desde Kilo, incluidas fechas anteriores a la instalación del proxy. Las filas son agregados diarios, cruzados por hasta tres dimensiones.')));
    const form=make('form');form.id='usage-query-form';
    const fields=make('div',undefined,'usage-query-fields');
    for(const [key,label] of [['startDate',L('From · UTC','Desde · UTC')],['endDate',L('Through · UTC','Hasta · UTC')]]) {
      const wrap=make('label',label),input=make('input');input.type='date';input.id='usage-'+key;input.required=true;input.value=draft[key];input.max=new Date().toISOString().slice(0,10);input.disabled=loading;input.addEventListener('input',()=>{draft[key]=input.value;});wrap.append(input);fields.append(wrap);
    }
    const costLabel=make('label',L('Cost basis','Base del coste')),cost=make('select');cost.id='usage-cost-source';
    for(const [value,label] of [['cost',L('Kilo charges','Cargos de Kilo')],['market',L('Market cost','Coste de mercado')]]){const option=make('option',label);option.value=value;cost.append(option);}
    cost.value=draft.costSource;cost.disabled=loading;cost.addEventListener('change',()=>{draft.costSource=cost.value;});costLabel.append(cost);fields.append(costLabel);form.append(fields);
    const groups=make('fieldset');groups.append(make('legend',L('Group each day by · choose up to 3','Cruzar cada día por · elige hasta 3')));
    const groupFields=make('div',undefined,'usage-query-groups');
    for(const dim of dimensions){const label=make('label'),check=make('input');check.type='checkbox';check.id='usage-group-'+dim;check.checked=draft.groupBy.includes(dim);check.disabled=loading || (draft.groupBy.length>=3&&!check.checked);check.addEventListener('change',()=>{draft.groupBy=dimensions.filter(value=>value===dim?check.checked:draft.groupBy.includes(value));for(const value of dimensions){const node=root.querySelector('#usage-group-'+value);node.disabled=draft.groupBy.length>=3&&!node.checked;}});label.append(check,doc.createTextNode(labels()[dim]));groupFields.append(label);}groups.append(groupFields);form.append(groups);
    const filters=make('details');filters.id='usage-filters';filters.append(make('summary',L('Filter by exact IDs','Filtrar por IDs exactos')),note(L('Comma-separated values; leave empty for all values. Up to 366 days per export. Only your usage in the selected account/team is queried.','Valores separados por comas; deja vacío para incluir todos. Hasta 366 días por exportación. Solo se consulta tu uso de la cuenta o equipo seleccionado.')));
    const filterFields=make('div',undefined,'usage-query-fields');
    for(const key of ['models','providers','projects','features','modes']){const label=make('label',labels()[key.slice(0,-1)]),input=make('input');input.id='usage-filter-'+key;input.value=draft[key];input.disabled=loading;input.addEventListener('input',()=>{draft[key]=input.value;});label.append(input);filterFields.append(label);}filters.append(filterFields);form.append(filters);
    const actions=make('div',undefined,'account-heading'),load=make('button',loading?L('Retrieving…','Recuperando…'):L('Retrieve history','Recuperar histórico'),'secondary-button');load.type='submit';load.id='usage-query';load.disabled=loading||!scope;
    const download=make('button',L('Download raw CSV','Descargar CSV raw'),'secondary-button');download.type='button';download.id='usage-download';download.disabled=loading||!result||result.scope!==scope;
    download.addEventListener('click',()=>{if(!result||result.scope!==scope)return;const url=URL.createObjectURL(new Blob([usageCSV(result)],{type:'text/csv;charset=utf-8'})),link=make('a');link.href=url;link.download=usageFilename(result.query);link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
    actions.append(load,download);form.append(actions);form.addEventListener('submit',async event=>{
      event.preventDefault();if(loading||!scope)return;
      const query={startDate:draft.startDate,endDate:draft.endDate,groupBy:[...draft.groupBy],costSource:draft.costSource};for(const key of ['models','providers','projects','features','modes'])query[key]=draft[key].split(',').map(value=>value.trim()).filter(Boolean);
      const revision=++generation,expectedScope=scope;loading=true;result=null;message='';render();
      try{const fresh=await api('usage/query',query);if(revision===generation&&expectedScope===scope&&fresh.scope===scope)result=fresh;}
      catch(error){if(revision===generation)message=error.message;}
      finally{if(revision===generation){loading=false;render();}}
    });root.append(form);
    const status=make('p',message||(!scope?L('Sign in to Kilo to retrieve historical usage.','Inicia sesión en Kilo para recuperar el histórico.'):(loading?L('Querying Kilo. Large ranges can take several requests.','Consultando Kilo. Los rangos amplios pueden necesitar varias consultas.'):(result?L(`${result.rows.length} rows · ${result.query.startDate} through ${result.query.endDate} · ${result.query.costSource} · UTC`,`${result.rows.length} filas · ${result.query.startDate} a ${result.query.endDate} · ${result.query.costSource} · UTC`):''))),'account-status');status.id='usage-query-status';status.setAttribute('role','status');status.hidden=!status.textContent;root.append(status);
    if(result){root.append(note(L('CSV contains every returned row, exact token counts and costMicrodollars (1 USD = 1,000,000). Preview: first 100 rows. Missing combinations are not filled with zeros.','El CSV incluye todas las filas devueltas, contadores exactos y costMicrodollars (1 USD = 1.000.000). Vista previa: primeras 100 filas. Las combinaciones ausentes no se rellenan con ceros.')));const wrap=make('div',undefined,'table-wrap'),table=make('table'),head=make('thead'),header=make('tr');result.columns.forEach(column=>{const th=make('th',column);th.scope='col';header.append(th);});head.append(header);const body=make('tbody');for(const row of result.rows.slice(0,100)){const tr=make('tr');row.forEach(value=>tr.append(make('td',value)));body.append(tr);}table.append(head,body);table.id='usage-query-table';wrap.append(table);root.append(wrap);}
  }
  return { update(state,nextLanguage){const nextScope=state?.usageAccountScope||'';const changed=nextScope!==scope||nextLanguage!==language||!initialized;if(nextScope!==scope){scope=nextScope;result=null;loading=false;message='';generation++;}language=nextLanguage;initialized=true;if(changed)render();} };
}
