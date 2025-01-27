/**
 * Copyright (c) HashiCorp, Inc.
 * SPDX-License-Identifier: MPL-2.0
 */

import Component from '@glimmer/component';
import { action } from '@ember/object';
import type PkiRoleModel from 'vault/models/pki/role';
import type PkiKeyModel from 'vault/models/pki/key';
import type PkiActionModel from 'vault/models/pki/action';
import type { HTMLElementEvent } from 'forms';
/**
 * @module PkiKeyParameters
 * PkiKeyParameters components are used to display a list of key bit options depending on the selected key type. The key bits field is disabled until a key type is selected.
 * If the component renders in a group, other attrs may be passed in and will be rendered using the <FormField> component
 * @example
 * ```js
 * <PkiKeyParameters @model={{@model}} @fields={{fields}}/>
 * ```
 * @param {class} model - The pki/role, pki/action, pki/key model.
 * @param {string} fields - Array of attributes from a formFieldGroup generated by the @withFormFields decorator ex: [{ name: 'attrName', type: 'string', options: {...} }]
 */
interface Args {
  model: PkiRoleModel | PkiKeyModel | PkiActionModel;
}
interface ModelAttributeName {
  keyType: string;
  keyBits: string;
}
interface TypeOptions {
  rsa: string;
  ec: string;
  ed25519: string;
  any: string;
}
interface BitOptions {
  [key: string]: Array<string>;
}

// first value in array is the default bits for that key type
const KEY_BITS_OPTIONS: BitOptions = {
  rsa: ['2048', '3072', '4096', '0'],
  ec: ['256', '224', '384', '521', '0'],
  ed25519: ['0'],
  any: ['0'],
};

export default class PkiKeyParameters extends Component<Args> {
  get keyBitOptions() {
    if (!this.args.model.keyType) return [];

    return KEY_BITS_OPTIONS[this.args.model.keyType];
  }

  @action handleSelection(name: string, selection: string) {
    this.args.model[name as keyof ModelAttributeName] = selection;

    if (name === 'keyType' && Object.keys(KEY_BITS_OPTIONS)?.includes(selection)) {
      const bitOptions = KEY_BITS_OPTIONS[selection as keyof TypeOptions];
      this.args.model.keyBits = bitOptions?.firstObject;
    }
  }

  @action onKeyBitsChange(event: HTMLElementEvent<HTMLInputElement>) {
    this.handleSelection(event.target.name, event.target.value);
  }
}
